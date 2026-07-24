package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"
)

// Config tunes the engine's timing (small values in tests).
type Config struct {
	PollInterval time.Duration // Observe poll cadence
	StageTimeout time.Duration // max wait per install phase
}

func (c Config) withDefaults() Config {
	if c.PollInterval <= 0 {
		c.PollInterval = 3 * time.Second
	}
	if c.StageTimeout <= 0 {
		c.StageTimeout = 30 * time.Minute
	}
	return c
}

// Manager runs deploys for all clusters. Hardware calls go through Executor;
// the agent's checkin/report advance the post-imaging stages.
type Manager struct {
	store *Store
	exec  Executor
	cfg   Config

	mu      sync.Mutex
	deploys map[string]*Deploy
	cancels map[string]context.CancelFunc
}

func NewManager(store *Store, exec Executor, cfg Config) *Manager {
	return &Manager{
		store:   store,
		exec:    exec,
		cfg:     cfg.withDefaults(),
		deploys: map[string]*Deploy{},
		cancels: map[string]context.CancelFunc{},
	}
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// Start begins (or restarts) a deploy of the given nodes for a cluster.
func (m *Manager) Start(clusterID string, nodes []Node) (*Deploy, error) {
	if len(nodes) == 0 {
		return nil, errors.New("no nodes to deploy")
	}
	m.mu.Lock()
	if cancel, ok := m.cancels[clusterID]; ok {
		cancel() // stop any prior run
	}
	d := &Deploy{ClusterID: clusterID, StartedAt: nowUTC(), Nodes: map[string]*NodeDeploy{}}
	for _, n := range nodes {
		d.Nodes[n.Hostname] = &NodeDeploy{
			Hostname:  n.Hostname,
			MachineID: n.MachineID,
			State:     StatePending,
			UpdatedAt: nowUTC(),
		}
	}
	m.deploys[clusterID] = d
	ctx, cancel := context.WithCancel(context.Background())
	m.cancels[clusterID] = cancel
	m.persistLocked(d)
	m.mu.Unlock()

	for _, n := range nodes {
		go m.runNode(ctx, clusterID, n)
	}
	return m.snapshot(clusterID)
}

func (m *Manager) Cancel(clusterID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancel, ok := m.cancels[clusterID]; ok {
		cancel()
		delete(m.cancels, clusterID)
	}
}

// Status returns the current deploy (loading from disk if not in memory).
func (m *Manager) Status(clusterID string) (*Deploy, error) {
	m.mu.Lock()
	_, ok := m.deploys[clusterID]
	m.mu.Unlock()
	if ok {
		return m.snapshot(clusterID)
	}
	return m.store.Load(clusterID)
}

// set mutates a node's deploy record under lock and persists it.
func (m *Manager) set(clusterID, hostname string, fn func(*NodeDeploy)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.deploys[clusterID]
	if d == nil {
		return
	}
	nd := d.Nodes[hostname]
	if nd == nil {
		return
	}
	fn(nd)
	nd.UpdatedAt = nowUTC()
	m.persistLocked(d)
}

func (m *Manager) persistLocked(d *Deploy) {
	if err := m.store.Save(d); err != nil {
		log.Printf("orchestrator: persist %s: %v", d.ClusterID, err)
	}
}

// snapshot returns a deep copy of a cluster's deploy for safe reading.
func (m *Manager) snapshot(clusterID string) (*Deploy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.deploys[clusterID]
	if d == nil {
		return nil, ErrNotFound
	}
	b, _ := json.Marshal(d)
	var cp Deploy
	json.Unmarshal(b, &cp)
	return &cp, nil
}

func (m *Manager) fail(clusterID, hostname string, err error) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		nd.State = StateError
		nd.Message = err.Error()
	})
}

func (m *Manager) advance(clusterID, hostname string, s State) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) { nd.State = s })
}

func (m *Manager) state(clusterID, hostname string) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.deploys[clusterID]; d != nil {
		if nd := d.Nodes[hostname]; nd != nil {
			return nd.State
		}
	}
	return ""
}

// agentDriven reports whether a state is advanced by the agent (so the
// orchestrator's imaging poll should stop).
func agentDriven(s State) bool {
	switch s {
	case StateCheckedIn, StateNetPreflight, StateApplying, StateApplied, StateDone, StateError:
		return true
	}
	return false
}

// runNode drives the orchestrator-side (IPMI + imaging) stages. Post-imaging
// stages are advanced by the agent's checkin/report.
func (m *Manager) runNode(ctx context.Context, clusterID string, n Node) {
	m.advance(clusterID, n.Hostname, StateBMCPreflight)
	if err := m.exec.Preflight(ctx, n); err != nil {
		m.fail(clusterID, n.Hostname, err)
		return
	}
	m.advance(clusterID, n.Hostname, StateSetBootPXE)
	if err := m.exec.SetBootPXE(ctx, n); err != nil {
		m.fail(clusterID, n.Hostname, err)
		return
	}
	m.advance(clusterID, n.Hostname, StatePowerCycle)
	if err := m.exec.PowerCycle(ctx, n); err != nil {
		m.fail(clusterID, n.Hostname, err)
		return
	}

	// Observe install progress until the media is fully fetched.
	m.advance(clusterID, n.Hostname, StateNetbooting)
	deadline := time.Now().Add(m.cfg.StageTimeout)
	ticker := time.NewTicker(m.cfg.PollInterval)
	defer ticker.Stop()
	sawImaging := false
	for {
		select {
		case <-ctx.Done():
			return // cancelled; leave state as-is
		case <-ticker.C:
		}
		// If the agent has already checked in (or beyond), imaging is done
		// from our perspective — stop polling.
		if s := m.state(clusterID, n.Hostname); agentDriven(s) {
			return
		}
		if time.Now().After(deadline) {
			m.fail(clusterID, n.Hostname, errors.New("timed out waiting for install"))
			return
		}
		stage, err := m.exec.Observe(ctx, n)
		if err != nil {
			continue // transient; keep polling until timeout
		}
		if stage == StageImaging && !sawImaging {
			sawImaging = true
			m.advance(clusterID, n.Hostname, StateImaging)
		}
		if stage == StageDone {
			m.advance(clusterID, n.Hostname, StateImaged)
			return // node now awaits the phone-home agent
		}
	}
}

// MarkCheckedIn records that the agent on a node has phoned home.
func (m *Manager) MarkCheckedIn(clusterID, hostname string) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		if nd.State == StateImaged || nd.State == StateNetbooting || nd.State == StateImaging {
			nd.State = StateCheckedIn
		}
	})
}

// Report applies an agent progress report to a node's deploy state.
func (m *Manager) Report(clusterID, hostname string, state State, message string, pf []PreflightResult) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		nd.State = state
		nd.Message = message
		if pf != nil {
			nd.Preflight = pf
		}
	})
}

var _ = terminal // reserved for future stepping guards
