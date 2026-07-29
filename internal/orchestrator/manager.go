package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// Config tunes the engine's timing (small values in tests).
type Config struct {
	PollInterval time.Duration // Observe poll cadence
	StageTimeout time.Duration // max wait per install phase
	SkewLimitSec float64       // max tolerated clock skew for green light 1
	// PowerStagger spaces out per-node power-ons in a batch (inspect/deploy) so
	// many servers don't draw simultaneous inrush. 0 = no stagger (tests).
	PowerStagger time.Duration
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
	gate     GateWriter
	cfg      Config
	// manualGate holds the peers at the apply gate: the master applying does NOT
	// auto-release them; the operator releases each via ReleaseNode.
	manualGate bool

	mu      sync.Mutex
	deploys map[string]*Deploy
	cancels map[string]context.CancelFunc
	// nodes keeps each run's Node (with BMC creds) by cluster→hostname, so the
	// master-first release can write the "go" SEL to non-masters' BMCs.
	nodes map[string]map[string]Node
	// setReady holds the operator's UI-supplied FTS finalize input per cluster;
	// the master agent polls it and runs `cluster set_ready`.
	setReady map[string]SetReadyInput
	// inspects tracks in-flight hardware-inspect boots by machine id.
	inspects map[string]*InspectStatus
}

// InspectStatus is the per-machine state of a hardware-inspect boot.
type InspectStatus struct {
	MachineID string `json:"machineId"`
	Label     string `json:"label"`
	State     string `json:"state"` // booting | reported | error
	Message   string `json:"message,omitempty"`
	UpdatedAt string `json:"updatedAt"`
}

// SetReadyInput is the operator's UI input for the one-time cluster set_ready
// (FTS finalize): create the shared external network + its CIDR/gateway/pool.
type SetReadyInput struct {
	Trigger        bool   `json:"trigger"`
	CreateExternal bool   `json:"createExternal"`
	CIDR           string `json:"cidr"`
	Gateway        string `json:"gateway"`
	IPRange        string `json:"ipRange"`
	// Ready reflects the master's result once set_ready has run.
	Ready   bool   `json:"ready"`
	Message string `json:"message,omitempty"`
}

func NewManager(store *Store, exec Executor, cfg Config) *Manager {
	return &Manager{
		store:    store,
		exec:     exec,
		verifier: PingVerifier{},
		cfg:      cfg.withDefaults(),
		deploys:  map[string]*Deploy{},
		cancels:  map[string]context.CancelFunc{},
		nodes:    map[string]map[string]Node{},
		setReady: map[string]SetReadyInput{},
		inspects: map[string]*InspectStatus{},
	}
}

// inspectCheckinTimeout bounds how long an inspect may sit "booting" with no
// check-in before it's marked errored (dead PSU, PXE failure, wrong network).
const inspectCheckinTimeout = 15 * time.Minute

// StartInspect force-PXEs + power-cycles each machine so it boots the installer
// in inventory mode (agent --inventory: report hardware, then halt). Progress is
// tracked per machine for the UI.
func (m *Manager) StartInspect(nodes []Node, labels map[string]string) {
	m.mu.Lock()
	for _, n := range nodes {
		m.inspects[n.MachineID] = &InspectStatus{
			MachineID: n.MachineID, Label: labels[n.MachineID], State: "booting", UpdatedAt: nowUTC(),
		}
	}
	m.mu.Unlock()
	for i, n := range nodes {
		go func(n Node, idx int) {
			// Stagger power-ons so a batch doesn't hit simultaneous inrush.
			time.Sleep(time.Duration(idx) * m.cfg.PowerStagger)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := m.exec.SetBootPXE(ctx, n); err != nil {
				m.setInspect(n.MachineID, "error", err.Error())
				return
			}
			if err := m.exec.PowerCycle(ctx, n); err != nil {
				m.setInspect(n.MachineID, "error", err.Error())
				return
			}
			// No-checkin timeout: if the node never boots to inspect (dead PSU,
			// PXE failure), don't leave it hanging "booting" — mark it error.
			time.Sleep(inspectCheckinTimeout)
			m.expireInspect(n.MachineID)
		}(n, i)
	}
}

// expireInspect marks an inspect as errored only if it never reported — a node
// that checked in and inventoried is already "reported" and left untouched.
func (m *Manager) expireInspect(machineID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.inspects[machineID]; s != nil && s.State == "booting" {
		s.State = "error"
		s.Message = "no check-in within timeout — node did not boot to inspect (check power / PXE)"
		s.UpdatedAt = nowUTC()
	}
}

func (m *Manager) setInspect(machineID, state, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.inspects[machineID]; s != nil {
		s.State = state
		s.Message = msg
		s.UpdatedAt = nowUTC()
	}
}

// IsInspecting reports whether a machine is in an active inspect boot (so the
// preflight checkin can tell it to inventory + halt rather than deploy).
func (m *Manager) IsInspecting(machineID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.inspects[machineID]
	return s != nil && s.State != "reported"
}

// InspectReported marks a machine's inspect complete (its hardware landed).
func (m *Manager) InspectReported(machineID string) { m.setInspect(machineID, "reported", "") }

// Inspects returns the current inspect statuses (for the UI).
func (m *Manager) Inspects() []InspectStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]InspectStatus, 0, len(m.inspects))
	for _, s := range m.inspects {
		out = append(out, *s)
	}
	return out
}

// SubmitSetReady stores the operator's set_ready input (from the UI) and arms
// the trigger the master agent is polling for.
func (m *Manager) SubmitSetReady(clusterID string, in SetReadyInput) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in.Trigger = true
	in.Ready = false // a fresh submit (new reimage) is not ready until it runs
	in.Message = ""
	m.setReady[clusterID] = in
	if err := m.store.SaveSetReady(clusterID, in); err != nil {
		log.Printf("orchestrator: persist set-ready %s: %v", clusterID, err)
	}
}

// GetSetReady returns the current set_ready input/status for a cluster, lazily
// loading the persisted value (so it's pre-filled on a later reimage / after a
// restart, not just within the session that submitted it).
//
// Trigger is gated on ALL cluster nodes having finished their snapshot apply:
// the master polls this and runs `cluster set_ready` only when Trigger is set,
// so finalize (external network, cluster start) runs against a COMPLETE
// cluster — never on a lone master while peers are still applying.
func (m *Manager) GetSetReady(clusterID string) SetReadyInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.setReady[clusterID]
	if !ok {
		if loaded, lok := m.store.LoadSetReady(clusterID); lok {
			m.setReady[clusterID] = loaded
			in = loaded
			ok = true
		}
	}
	if !ok {
		return SetReadyInput{}
	}
	if in.Trigger && !m.allNodesAppliedLocked(clusterID) {
		in.Trigger = false // hold set_ready until every node is done
	}
	return in
}

// allNodesAppliedLocked reports whether every node in the active deploy has
// finished applying (reached done). No active deploy → true (nothing to wait
// for, e.g. a standalone re-arm after a restart).
func (m *Manager) allNodesAppliedLocked(clusterID string) bool {
	d := m.deploys[clusterID]
	if d == nil || len(d.Nodes) == 0 {
		return true
	}
	for _, nd := range d.Nodes {
		if nd.State != StateDone {
			return false
		}
	}
	return true
}

// MarkReady records the master's set_ready result (cluster ready or not).
func (m *Manager) MarkReady(clusterID string, ok bool, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in := m.setReady[clusterID]
	in.Ready = ok
	in.Message = msg
	m.setReady[clusterID] = in
	if err := m.store.SaveSetReady(clusterID, in); err != nil {
		log.Printf("orchestrator: persist set-ready %s: %v", clusterID, err)
	}
}

// SetVerifier overrides the Tier-2 cluster reachability checker (tests inject
// a fake).
func (m *Manager) SetVerifier(v Verifier) { m.verifier = v }

// SetSELObserver enables the out-of-band SEL status poll (nil = disabled, the
// default, so CI never contacts a real BMC).
func (m *Manager) SetSELObserver(o SELObserver) { m.sel = o }

// SetGateWriter enables writing the master-done "go" SEL to non-master BMCs
// (nil = disabled).
func (m *Manager) SetGateWriter(g GateWriter) { m.gate = g }

// SetManualGate enables operator-gated, one-node-at-a-time apply release: the
// master applying no longer auto-releases the peers; each is released by hand
// via ReleaseNode after the operator confirms.
func (m *Manager) SetManualGate(b bool) { m.manualGate = b }

// ReleaseNode writes the master-done "go" SEL to a single node's BMC, releasing
// just that node to apply. Used for manual, sequential (one-by-one) reimage.
func (m *Manager) ReleaseNode(clusterID, hostname string) error {
	m.mu.Lock()
	var target Node
	found := false
	if byHost := m.nodes[clusterID]; byHost != nil {
		if n, ok := byHost[hostname]; ok {
			target, found = n, true
		}
	}
	gate := m.gate
	m.mu.Unlock()
	if !found {
		return fmt.Errorf("node %s not in deploy %s", hostname, clusterID)
	}
	if gate == nil {
		return errors.New("no gate writer configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := gate.WriteGate(ctx, target); err != nil {
		return fmt.Errorf("release %s via SEL go: %w", hostname, err)
	}
	log.Printf("manual release %s: wrote 'go' SEL to %s", hostname, target.BMCAddress)
	return nil
}

// HasActiveDeploy reports whether a cluster has a non-terminal deploy running —
// used to resolve which of a multi-assigned machine's clusters is deploying.
func (m *Manager) HasActiveDeploy(clusterID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.deploys[clusterID]
	if d == nil {
		return false
	}
	for _, nd := range d.Nodes {
		if nd.State != StateDone && nd.State != StateError {
			return true
		}
	}
	return false
}

// Master returns the master hostname for a running deploy (empty if unknown).
func (m *Manager) Master(clusterID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.deploys[clusterID]; d != nil {
		return d.Master
	}
	return ""
}

// Applied records that a node finished applying its (local) snapshot. When the
// master reports applied, the server releases every non-master by writing the
// "go" SEL record to its BMC over LAN — the OOB master-first handoff.
func (m *Manager) Applied(clusterID, hostname string, isMaster bool) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) { nd.State = StateDone })
	if !isMaster {
		return
	}
	if m.manualGate {
		log.Printf("master %s applied; manual gate on — peers held for operator release", hostname)
		return
	}
	m.mu.Lock()
	byHost := m.nodes[clusterID]
	master := ""
	if d := m.deploys[clusterID]; d != nil {
		master = d.Master
	}
	var targets []Node
	for host, n := range byHost {
		if host != master {
			targets = append(targets, n)
		}
	}
	gate := m.gate
	m.mu.Unlock()

	if gate == nil {
		return
	}
	for _, n := range targets {
		go func(n Node) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := gate.WriteGate(ctx, n); err != nil {
				log.Printf("release %s via SEL go: %v", n.Hostname, err)
			} else {
				log.Printf("released %s: wrote master-done 'go' SEL to %s", n.Hostname, n.BMCAddress)
			}
		}(n)
	}
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// Start begins (or restarts) a deploy of the given nodes for a cluster.
// master is the hostname whose FTS must finish before other nodes apply.
func (m *Manager) Start(clusterID string, nodes []Node, master string, verifyTargets []string, manual bool) (*Deploy, error) {
	if len(nodes) == 0 {
		return nil, errors.New("no nodes to deploy")
	}
	m.mu.Lock()
	if cancel, ok := m.cancels[clusterID]; ok {
		cancel() // stop any prior run
	}
	step := 0
	if manual {
		step = StepPreflight
	}
	d := &Deploy{ClusterID: clusterID, StartedAt: nowUTC(), Master: master, VerifyTargets: verifyTargets, Manual: manual, ManualStep: step, Nodes: map[string]*NodeDeploy{}}
	byHost := map[string]Node{}
	for _, n := range nodes {
		d.Nodes[n.Hostname] = &NodeDeploy{
			Hostname:  n.Hostname,
			MachineID: n.MachineID,
			State:     StatePending,
			UpdatedAt: nowUTC(),
		}
		byHost[n.Hostname] = n
	}
	m.nodes[clusterID] = byHost
	m.deploys[clusterID] = d
	ctx, cancel := context.WithCancel(context.Background())
	m.cancels[clusterID] = cancel
	m.persistLocked(d)
	m.mu.Unlock()

	for i, n := range nodes {
		go func(idx int, n Node) {
			// Stagger power-ons across nodes (respecting cancellation).
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(idx) * m.cfg.PowerStagger):
			}
			m.runNode(ctx, clusterID, n)
		}(i, n)
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
		nd.Progress = nd.progress()
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
	// Ignore SEL records older than this deploy: a previous run's terminal
	// record would otherwise replay into a fresh deploy and complete it
	// instantly. 5min grace absorbs BMC clock skew (time-sync gate is ±5s).
	var since time.Time
	m.mu.Lock()
	if d := m.deploys[clusterID]; d != nil {
		if t, err := time.Parse(time.RFC3339, d.StartedAt); err == nil {
			since = t.Add(-5 * time.Minute)
		}
	}
	m.mu.Unlock()
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
			if !since.IsZero() && s.At.Before(since) {
				continue // stale record from a previous deploy
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

// RestoreDone advances a node from restoring to rebooting. The installer reports
// this in-band right before it reboots the freshly-imaged node, so the progress
// strip can show restore-complete (green) and reboot (yellow) distinctly instead
// of a single opaque "restoring".
func (m *Manager) RestoreDone(clusterID, hostname string) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		if nd.State == StateRestoring {
			nd.State = StateRebooting
		}
	})
}

// ApplyStarted marks the OS-phase agent up and applying: the reboot completed
// and the snapshot apply is in progress. Moves the node off "rebooting" so the
// UI flips reboot → done and apply → active during the long FTS apply.
func (m *Manager) ApplyStarted(clusterID, hostname string) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		if nd.State == StateRebooting {
			nd.State = StateApplying
		}
	})
}

// ApplyFailed marks a node's snapshot apply as terminally failed (real failure,
// or did not converge after the bounded two-phase reboots) — the UI shows the
// node errored (apply cell red) instead of hanging on rebooting/applying.
func (m *Manager) ApplyFailed(clusterID, hostname, msg string) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		nd.State = StateError
		nd.ErrCode = ErrApplyFailed
		nd.Message = msg
	})
}

// RekickPreflight asks a parked installer agent to redo its preflight from
// check-in (fresh bundle + snapshot) — used after the operator fixes the
// cluster config. In-place: no PXE reboot. Valid only while the node is still
// in the preflight phase.
func (m *Manager) RekickPreflight(clusterID, hostname string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.deploys[clusterID]
	if d == nil {
		return fmt.Errorf("no active deploy for cluster %s", clusterID)
	}
	nd := d.Nodes[hostname]
	if nd == nil {
		return fmt.Errorf("node %s is not part of this deploy", hostname)
	}
	switch nd.State {
	case StateNetbooting, StatePreflighting, StatePreflightOK:
	default:
		return fmt.Errorf("node %s is %s — past preflight, a re-run needs a re-image", hostname, nd.State)
	}
	nd.RekickSeq++
	if nd.InstallerPreflight != nil {
		nd.InstallerPreflight.Passed = false // re-blocks green light 1 until the re-run passes
	}
	nd.State = StatePreflighting
	nd.Message = "preflight re-run requested"
	nd.UpdatedAt = nowUTC()
	m.persistLocked(d)
	return nil
}

// RekickSeq returns the node's current preflight re-kick sequence (0 if none).
func (m *Manager) RekickSeq(clusterID, hostname string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.deploys[clusterID]; d != nil {
		if nd := d.Nodes[hostname]; nd != nil {
			return nd.RekickSeq
		}
	}
	return 0
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
	// Manual mode: also hold until the operator advances past the preflight step.
	if d.Manual && d.ManualStep < StepRestore {
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

// RebootProceed gates the installer between restore-done and reboot. In manual
// mode it holds until the operator advances past the restore step; otherwise
// (and in auto mode) it always proceeds.
func (m *Manager) RebootProceed(clusterID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.deploys[clusterID]
	if d == nil {
		return true
	}
	return !d.Manual || d.ManualStep >= StepReboot
}

// ApplyProceed gates the master's OS-phase snapshot apply. In manual mode it
// holds until the operator advances to the apply-master step. Non-masters are
// gated separately by the master-first SEL 'go'.
func (m *Manager) ApplyProceed(clusterID, hostname string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.deploys[clusterID]
	if d == nil {
		return true
	}
	if !d.Manual {
		return true
	}
	if hostname == d.Master {
		return d.ManualStep >= StepApplyMaster
	}
	// Peers proceed once the operator authorizes apply-rest.
	return d.ManualStep >= StepApplyRest
}

// AdvanceStep moves a manual deploy to the next step (operator "Next"). Returns
// the new step. No-op for an auto deploy or when already at the final step.
func (m *Manager) AdvanceStep(clusterID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.deploys[clusterID]
	if d == nil {
		return 0, fmt.Errorf("no active deploy for cluster %s", clusterID)
	}
	if !d.Manual {
		return 0, fmt.Errorf("deploy for cluster %s is not in manual mode", clusterID)
	}
	if d.ManualStep < StepApplyRest {
		d.ManualStep++
		m.persistLocked(d)
	}
	return d.ManualStep, nil
}

// ManualState returns whether the deploy is manual and its current step.
func (m *Manager) ManualState(clusterID string) (manual bool, step int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.deploys[clusterID]; d != nil {
		return d.Manual, d.ManualStep
	}
	return false, 0
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
