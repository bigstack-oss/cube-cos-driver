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
	SkewLimitSec float64       // max tolerated clock skew for green light 1
}

func (c Config) withDefaults() Config {
	if c.PollInterval <= 0 {
		c.PollInterval = 3 * time.Second
	}
	if c.StageTimeout <= 0 {
		c.StageTimeout = 30 * time.Minute
	}
	if c.SkewLimitSec <= 0 {
		c.SkewLimitSec = 5
	}
	return c
}

// Manager runs deploys for all clusters. Hardware calls go through Executor;
// the agent's checkin/report advance the post-imaging stages.
type Manager struct {
	store    *Store
	exec     Executor
	verifier Verifier
	sel      SELObserver
	cfg      Config

	mu      sync.Mutex
	deploys map[string]*Deploy
	cancels map[string]context.CancelFunc
}

func NewManager(store *Store, exec Executor, cfg Config) *Manager {
	return &Manager{
		store:    store,
		exec:     exec,
		verifier: PingVerifier{},
		cfg:      cfg.withDefaults(),
		deploys:  map[string]*Deploy{},
		cancels:  map[string]context.CancelFunc{},
	}
}

// SetVerifier overrides the Tier-2 cluster reachability checker (tests inject
// a fake).
func (m *Manager) SetVerifier(v Verifier) { m.verifier = v }

// SetSELObserver enables the out-of-band SEL status poll (nil = disabled, the
// default, so CI never contacts a real BMC).
func (m *Manager) SetSELObserver(o SELObserver) { m.sel = o }

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// Start begins (or restarts) a deploy of the given nodes for a cluster.
// master is the hostname whose FTS must finish before other nodes apply.
func (m *Manager) Start(clusterID string, nodes []Node, master string, verifyTargets []string) (*Deploy, error) {
	if len(nodes) == 0 {
		return nil, errors.New("no nodes to deploy")
	}
	m.mu.Lock()
	if cancel, ok := m.cancels[clusterID]; ok {
		cancel() // stop any prior run
	}
	d := &Deploy{ClusterID: clusterID, StartedAt: nowUTC(), Master: master, VerifyTargets: verifyTargets, Nodes: map[string]*NodeDeploy{}}
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
	if m.sel != nil {
		go m.pollSEL(ctx, clusterID, nodes)
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
	for _, nd := range cp.Nodes {
		nd.Light1, nd.Light2 = nd.lights()
		nd.Phase = nd.phase()
	}
	return &cp, nil
}

func (m *Manager) fail(clusterID, hostname string, code ErrCode, err error) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		if agentDriven(nd.State) {
			return // agent has taken over; don't regress
		}
		nd.State = StateError
		nd.ErrCode = code
		nd.Message = err.Error()
	})
}

// advance moves an orchestrator-driven node forward, but never regresses past
// a state the agent already set (the agent's reports are authoritative).
func (m *Manager) advance(clusterID, hostname string, s State) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		if agentDriven(nd.State) {
			return
		}
		nd.State = s
	})
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
	case StatePreflighting, StatePreflightOK, StateRestoring, StateRebooting,
		StateCheckedIn, StateWaiting, StateNetPreflight, StateApplying, StateApplied, StateDone, StateError:
		return true
	}
	return false
}

// pollSEL periodically reads each node's out-of-band status from its BMC and
// merges it, so the orchestrator still learns a node reached applied/done even
// if the in-band report is lost (e.g. mgmt moved off the flat L2 after apply).
// Stops when the context is cancelled or every node is terminal.
func (m *Manager) pollSEL(ctx context.Context, clusterID string, nodes []Node) {
	ticker := time.NewTicker(m.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		allTerminal := true
		for _, n := range nodes {
			if terminal(m.state(clusterID, n.Hostname)) {
				continue
			}
			allTerminal = false
			s, err := m.sel.Observe(ctx, n)
			if err != nil || s == nil {
				continue
			}
			m.MergeSEL(clusterID, n.Hostname, *s)
		}
		if allTerminal {
			return
		}
	}
}

// runNode drives the orchestrator-side (IPMI + imaging) stages. Post-imaging
// stages are advanced by the agent's checkin/report.
func (m *Manager) runNode(ctx context.Context, clusterID string, n Node) {
	m.advance(clusterID, n.Hostname, StateBMCPreflight)
	if err := m.exec.Preflight(ctx, n); err != nil {
		m.fail(clusterID, n.Hostname, ErrBMCUnreachable, err)
		return
	}
	m.advance(clusterID, n.Hostname, StateSetBootPXE)
	if err := m.exec.SetBootPXE(ctx, n); err != nil {
		m.fail(clusterID, n.Hostname, ErrBMCBootdev, err)
		return
	}
	m.advance(clusterID, n.Hostname, StatePowerCycle)
	if err := m.exec.PowerCycle(ctx, n); err != nil {
		m.fail(clusterID, n.Hostname, ErrBMCPower, err)
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
			m.fail(clusterID, n.Hostname, ErrPXETimeout, errors.New("timed out waiting for install"))
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

// PreflightReport records an installer-phase (pre-restore) validation result
// for a node. On pass the node advances to preflight-ok; otherwise it stays at
// preflighting (still converging) with a diagnostic error code explaining why
// green light 1 is being withheld.
func (m *Manager) PreflightReport(clusterID, hostname string, pf NodePreflight) {
	pf.ReportedAt = nowUTC()
	code := m.preflightCode(pf)
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		nd.InstallerPreflight = &pf
		if pf.Passed {
			nd.State = StatePreflightOK
			nd.ErrCode = ""
			nd.Message = ""
		} else {
			nd.State = StatePreflighting
			nd.ErrCode = code
		}
	})
}

// PreflightFail marks a node's preflight as terminally failed (e.g. the agent
// gave up after the matrix never converged).
func (m *Manager) PreflightFail(clusterID, hostname string, code ErrCode, msg string) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		nd.State = StateError
		nd.ErrCode = code
		nd.Message = msg
	})
}

// preflightCode returns the diagnostic code for a non-passing preflight result.
func (m *Manager) preflightCode(pf NodePreflight) ErrCode {
	if pf.Passed {
		return ""
	}
	if !pf.CarrierOK {
		return ErrPFCarrier
	}
	if pf.ClockSkewSec > m.cfg.SkewLimitSec || pf.ClockSkewSec < -m.cfg.SkewLimitSec {
		return ErrPFSkew
	}
	for _, r := range pf.Matrix {
		if !r.OK {
			return ErrPFPing
		}
	}
	return ""
}

// PreflightProgress marks a node as actively preflighting (topology up, matrix
// converging) without a terminal result.
func (m *Manager) PreflightProgress(clusterID, hostname string) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		if nd.State == StateNetbooting || nd.State == StateImaged || nd.State == "" {
			nd.State = StatePreflighting
		}
	})
}

// GreenLight1 reports whether a node may proceed from preflight to restore.
// It is a whole-cluster barrier: every node has passed its own preflight
// (carrier + ping matrix) AND the fleet clock skew is within the limit. When
// cleared, the node advances to restoring.
func (m *Manager) GreenLight1(clusterID, hostname string) (clear bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.deploys[clusterID]
	if d == nil {
		return false
	}
	clear = m.fabricReadyLocked(d)
	if clear {
		if nd := d.Nodes[hostname]; nd != nil && (nd.State == StatePreflightOK || nd.State == StatePreflighting) {
			nd.State = StateRestoring
			nd.UpdatedAt = nowUTC()
			m.persistLocked(d)
		}
	}
	return clear
}

// fabricReadyLocked is the green-light-1 barrier predicate.
func (m *Manager) fabricReadyLocked(d *Deploy) bool {
	for _, nd := range d.Nodes {
		pf := nd.InstallerPreflight
		if pf == nil || !pf.Passed || !pf.CarrierOK {
			return false
		}
		if pf.ClockSkewSec > m.cfg.SkewLimitSec || pf.ClockSkewSec < -m.cfg.SkewLimitSec {
			return false
		}
	}
	return true
}

// CheckIn records that a freshly-installed node's agent phoned home and returns
// whether it must hold (green light 2). The preflight barrier already ran
// pre-restore, so this gate is purely master-first: a non-master holds until
// the master reaches done.
func (m *Manager) CheckIn(clusterID, hostname string) (hold bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.deploys[clusterID]
	if d == nil {
		return false
	}
	masterDone := d.Master == "" || (d.Nodes[d.Master] != nil && d.Nodes[d.Master].State == StateDone)
	hold = hostname != d.Master && !masterDone
	if nd := d.Nodes[hostname]; nd != nil {
		switch nd.State {
		case StateRestoring, StateRebooting, StateNetbooting, StateImaging, StateImaged, StateWaiting, StateCheckedIn:
			if hold {
				nd.State = StateWaiting
			} else {
				nd.State = StateCheckedIn
			}
			nd.UpdatedAt = nowUTC()
			m.persistLocked(d)
		}
	}
	return hold
}

// Report applies an agent progress report to a node's deploy state. When the
// report brings every node to done, it triggers the Tier-2 whole-cluster
// reachability check.
func (m *Manager) Report(clusterID, hostname string, state State, message string, pf []PreflightResult) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		nd.State = state
		nd.Message = message
		if pf != nil {
			nd.Preflight = pf
		}
	})
	if state == StateDone {
		m.maybeVerifyCluster(clusterID)
	}
}

// maybeVerifyCluster runs the cluster reachability test once — gated on every
// node having reached done.
func (m *Manager) maybeVerifyCluster(clusterID string) {
	m.mu.Lock()
	d := m.deploys[clusterID]
	if d == nil || d.ClusterReady {
		m.mu.Unlock()
		return
	}
	for _, nd := range d.Nodes {
		if nd.State != StateDone {
			m.mu.Unlock()
			return // not all done yet
		}
	}
	targets := append([]string(nil), d.VerifyTargets...)
	m.mu.Unlock()

	go func() {
		var results []PreflightResult
		if m.verifier != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			results = m.verifier.Verify(ctx, targets)
			cancel()
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		d := m.deploys[clusterID]
		if d == nil {
			return
		}
		d.Verify = results
		d.ClusterReady = true
		for _, r := range results {
			if !r.OK {
				d.ClusterReady = false
			}
		}
		m.persistLocked(d)
	}()
}

var _ = terminal // reserved for future stepping guards
