package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
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
	// GateRecheck is how often the driver re-verifies that a node's authorized
	// gate records are still on its BMC, and re-writes any that vanished.
	GateRecheck time.Duration
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
	if c.GateRecheck <= 0 {
		c.GateRecheck = 30 * time.Second
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
	// advertise is the driver's node-reachable endpoint (IPv4:port), stamped
	// into each booting node's BMC SEL so the agent phones home to THIS driver
	// regardless of the shared PXE entry's driver_server=. Zero IP = disabled.
	advertiseIP   [4]byte
	advertisePort uint16
	// pxe flips the PXE default to an operator-picked image for a deploy/inspect
	// and restores it after the nodes boot. nil = image selection disabled.
	pxe PXEFlipper

	// wg tracks a deploy's background goroutines (per-node drivers + SEL poll) so
	// StopAll can wait for them to exit — otherwise they can still write the
	// deploy store after a caller (e.g. a test's temp-dir cleanup) tears down.
	wg sync.WaitGroup

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
	// inspectNodes keeps each in-flight inspect's Node (BMC creds) so the
	// persistent Force-PXE armed for the boot can be reset once it's terminal.
	inspectNodes map[string]Node
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
		store:        store,
		exec:         exec,
		verifier:     PingVerifier{},
		cfg:          cfg.withDefaults(),
		deploys:      map[string]*Deploy{},
		cancels:      map[string]context.CancelFunc{},
		nodes:        map[string]map[string]Node{},
		setReady:     map[string]SetReadyInput{},
		inspects:     map[string]*InspectStatus{},
		inspectNodes: map[string]Node{},
	}
}

// inspectCheckinTimeout bounds how long an inspect may sit "booting" with no
// check-in before it's marked errored (dead PSU, PXE failure, wrong network).
const inspectCheckinTimeout = 15 * time.Minute

// StartInspect force-PXEs + power-cycles each machine so it boots the installer
// in inventory mode (agent --inventory: report hardware, then halt). Progress is
// tracked per machine for the UI.
func (m *Manager) StartInspect(nodes []Node, labels map[string]string, image string) error {
	m.mu.Lock()
	for _, n := range nodes {
		m.inspects[n.MachineID] = &InspectStatus{
			MachineID: n.MachineID, Label: labels[n.MachineID], State: "booting", UpdatedAt: nowUTC(),
		}
		m.inspectNodes[n.MachineID] = n
	}
	m.mu.Unlock()
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.MachineID
	}
	// Repoint the PXE default to the picked inspect image; restore once every
	// inspected machine has booted (reported) or errored. Abort if locked.
	// Inspect arms driver_server= (so the installer runs the inspect-check +
	// phones home) but NOT autoinstall — an inspect must never restore a disk.
	inspectArm := ""
	if url := m.driverURL(); url != "" {
		inspectArm = "driver_server=" + url
	}
	if err := m.flipForBoot(context.Background(), image, inspectArm, func() bool { return m.inspectsBooted(ids) }); err != nil {
		for _, n := range nodes {
			m.setInspect(n.MachineID, "error", err.Error())
		}
		return err
	}
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
			m.stampEndpoint(ctx, n) // tell the node which driver booted it
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
	return nil
}

// inspectsBooted reports whether every listed machine has finished its inspect
// boot — reported inventory or errored (so the PXE flip can be restored).
func (m *Manager) inspectsBooted(machineIDs []string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range machineIDs {
		s := m.inspects[id]
		if s == nil {
			continue
		}
		if s.State != "reported" && s.State != "error" {
			return false
		}
	}
	return true
}

// expireInspect marks an inspect as errored only if it never reported — a node
// that checked in and inventoried is already "reported" and left untouched.
func (m *Manager) expireInspect(machineID string) {
	m.mu.Lock()
	expired := false
	if s := m.inspects[machineID]; s != nil && s.State == "booting" {
		s.State = "error"
		s.Message = "no check-in within timeout — node did not boot to inspect (check power / PXE)"
		s.UpdatedAt = nowUTC()
		expired = true
	}
	m.mu.Unlock()
	if expired {
		go m.resetInspectBoot(machineID)
	}
}

func (m *Manager) setInspect(machineID, state, msg string) {
	m.mu.Lock()
	if s := m.inspects[machineID]; s != nil {
		s.State = state
		s.Message = msg
		s.UpdatedAt = nowUTC()
	}
	m.mu.Unlock()
	if state == "reported" || state == "error" {
		go m.resetInspectBoot(machineID)
	}
}

// resetInspectBoot clears the persistent Force-PXE armed for an inspect boot.
// Without it the node re-PXEs into the installer on its next power-on instead
// of booting its disk (deploys reset on restore-done; inspects must too).
func (m *Manager) resetInspectBoot(machineID string) {
	m.mu.Lock()
	n, ok := m.inspectNodes[machineID]
	delete(m.inspectNodes, machineID)
	m.mu.Unlock()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := m.exec.SetBootDisk(ctx, n); err != nil {
		log.Printf("inspect %s: resetting boot device to disk failed: %v", machineID, err)
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
	// The network params only describe the shared external network — never keep
	// them without it, so no client can persist a createExternal=false + params
	// inconsistency (and set_ready isn't handed args that would force-create the
	// network against the operator's choice).
	if !in.CreateExternal {
		in.CIDR, in.Gateway, in.IPRange = "", "", ""
	}
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
// HasSetReady reports whether an operator set_ready spec exists for the
// cluster (submitted this run or persisted by a previous one).
func (m *Manager) HasSetReady(clusterID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.setReady[clusterID]; ok {
		return true
	}
	_, ok := m.store.LoadSetReady(clusterID)
	return ok
}

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
	// Manual mode: also hold set_ready until the operator advances to its step
	// (peers apply at StepApplyRest; set_ready is the separate final step).
	if d := m.deploys[clusterID]; d != nil && d.Manual && d.ManualStep < StepSetReady {
		in.Trigger = false
	}
	return in
}

// maybeAutoSetReady, in AUTO mode, writes the master's set-ready gate/go once
// every node has applied — the offline trigger for the master's cluster
// set_ready (the master waits on this SEL, not the network). Manual mode uses
// AdvanceStep(StepSetReady) instead. Writing the go again is harmless.
func (m *Manager) maybeAutoSetReady(clusterID string) {
	m.mu.Lock()
	d := m.deploys[clusterID]
	if d == nil || d.Manual || !m.allNodesAppliedLocked(clusterID) {
		m.mu.Unlock()
		return
	}
	m.writeGosAsync(clusterID, m.gateTargetsLocked(clusterID, "master"), gateStageSetReady)
	m.mu.Unlock()
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
	// Stamp the live deploy so the master's set-ready cell greens on set_ready
	// completion (not the earlier cluster-health check).
	if d := m.deploys[clusterID]; d != nil {
		d.SetReadyDone = ok
		m.persistLocked(d)
	}
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

// SetAdvertise sets the driver's node-reachable endpoint (stamped into node
// SEL at boot). ip zero-value disables stamping.
func (m *Manager) SetAdvertise(ip [4]byte, port uint16) { m.advertiseIP, m.advertisePort = ip, port }

// PXEFlipper points the PXE default at the chosen image (or the current default
// when image=="") and injects armArgs — the zero-touch arming — into that
// entry, under the shared advisory lock. The restore func strips the args, puts
// the default back, and releases the lock. So the shared entry is armed only
// while this driver's deploy/inspect is booting.
type PXEFlipper interface {
	Flip(image, armArgs string) (restore func(), err error)
}

// driverURL is this driver's node-reachable HTTP endpoint (from --advertise),
// or "" if unset. Injected as driver_server= so a booting node phones home to
// THIS driver even on the shared PXE entry.
func (m *Manager) driverURL() string {
	if m.advertiseIP == ([4]byte{}) {
		return ""
	}
	return fmt.Sprintf("http://%d.%d.%d.%d:%d",
		m.advertiseIP[0], m.advertiseIP[1], m.advertiseIP[2], m.advertiseIP[3], m.advertisePort)
}

// SetPXEFlipper enables operator image selection for deploy/inspect.
func (m *Manager) SetPXEFlipper(p PXEFlipper) { m.pxe = p }

// flipForBoot repoints the PXE default to image (if selection is enabled and an
// image was picked) and starts a watcher that restores the default once every
// node in nodes has booted the image (reached preflight or errored) or the
// stage timeout elapses. Returns an error only if the flip itself fails (e.g.
// the advisory lock is held) — the caller aborts the run so nodes don't boot a
// stale default. A no-op (nil pxe / empty image) returns nil.
func (m *Manager) flipForBoot(ctx context.Context, image, armArgs string, booted func() bool) error {
	if m.pxe == nil {
		return nil
	}
	restore, err := m.pxe.Flip(image, armArgs)
	if err != nil {
		return err
	}
	if restore == nil {
		return nil
	}
	go func() {
		deadline := time.Now().Add(m.cfg.StageTimeout + time.Minute)
		t := time.NewTicker(m.cfg.PollInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				restore()
				return
			case <-t.C:
			}
			if booted() || time.Now().After(deadline) {
				log.Printf("orchestrator: all nodes booted (or timeout) — restoring PXE default")
				restore()
				return
			}
		}
	}()
	return nil
}

// deployNodesBooted reports whether every node in a deploy has loaded the PXE
// image — reached preflight (rank>=1) or terminally failed (won't reboot into
// the picked image, so the flip can be released).
func (m *Manager) deployNodesBooted(clusterID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.deploys[clusterID]
	if d == nil {
		return true
	}
	for _, nd := range d.Nodes {
		if pipelineRank(nd.State) < 1 && nd.State != StateError {
			return false
		}
	}
	return true
}

// EndpointWriter stamps the driver's endpoint into a node's BMC SEL.
type EndpointWriter interface {
	WriteEndpoint(ctx context.Context, n Node, ip [4]byte, port uint16) error
}

// stampEndpoint writes the driver-endpoint SEL record to a node's BMC when an
// advertise address is configured and the gate writer supports it. Best-effort.
func (m *Manager) stampEndpoint(ctx context.Context, n Node) {
	if m.advertiseIP == ([4]byte{}) {
		return
	}
	ew, ok := m.gate.(EndpointWriter)
	if !ok {
		return
	}
	if err := ew.WriteEndpoint(ctx, n, m.advertiseIP, m.advertisePort); err != nil {
		log.Printf("orchestrator: stamp endpoint on %s: %v", n.Hostname, err)
	}
}

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
	if found && gate != nil {
		m.authorizeGatesLocked(clusterID, []Node{target}, gateStageApply)
	}
	stages := m.gateSetLocked(clusterID, hostname)
	if len(stages) == 0 {
		stages = []byte{gateStageApply} // node not in the deploy record; release anyway
	}
	m.mu.Unlock()
	if !found {
		return fmt.Errorf("node %s not in deploy %s", hostname, clusterID)
	}
	if gate == nil {
		return errors.New("no gate writer configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := gate.WriteGate(ctx, target, stages...); err != nil {
		return fmt.Errorf("release %s via SEL go: %w", hostname, err)
	}
	log.Printf("manual release %s: wrote gate stages=%v to %s", hostname, stages, target.BMCAddress)
	return nil
}

// gateSetLocked returns a node's authorized gate stages. Caller must hold m.mu.
func (m *Manager) gateSetLocked(clusterID, hostname string) []byte {
	if d := m.deploys[clusterID]; d != nil {
		if nd := d.Nodes[hostname]; nd != nil {
			return gateBytes(nd.Gates)
		}
	}
	return nil
}

// authorizeGatesLocked records `stage` in each target's gate ledger. Caller
// must hold m.mu; pair it with flushGatesAsync to put the ledger on the BMCs.
func (m *Manager) authorizeGatesLocked(clusterID string, targets []Node, stage byte) {
	d := m.deploys[clusterID]
	if d == nil {
		return
	}
	for _, n := range targets {
		nd := d.Nodes[n.Hostname]
		if nd == nil {
			continue
		}
		if !slices.Contains(nd.Gates, int(stage)) {
			nd.Gates = append(nd.Gates, int(stage))
		}
	}
	m.persistLocked(d)
}

// gateBytes converts a persisted gate ledger to the writer's stage bytes.
func gateBytes(gates []int) []byte {
	out := make([]byte, 0, len(gates))
	for _, g := range gates {
		out = append(out, byte(g))
	}
	return out
}

// flushGatesAsync writes each target's whole authorized set to its BMC in the
// background (best-effort), one call per node. Passing the full set — not just
// the newest stage — is what lets a SEL that lost records heal. Caller must
// hold m.mu; the IPMI work happens off-lock.
func (m *Manager) flushGatesAsync(clusterID string, targets []Node) {
	gate := m.gate
	d := m.deploys[clusterID]
	if gate == nil || d == nil {
		return
	}
	for _, n := range targets {
		nd := d.Nodes[n.Hostname]
		if nd == nil || len(nd.Gates) == 0 {
			continue
		}
		stages := gateBytes(nd.Gates)
		go func(n Node, stages []byte) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			err := gate.WriteGate(ctx, n, stages...)
			m.ackGates(clusterID, n.Hostname, stages, err)
			if err != nil {
				log.Printf("gate go stages=%v %s: %v", stages, n.Hostname, err)
			} else {
				log.Printf("wrote gate go stages=%v to %s (%s)", stages, n.Hostname, n.BMCAddress)
			}
		}(n, stages)
	}
}

// writeGosAsync authorizes `stage` for each target and puts their ledgers on
// the BMCs. Caller must hold m.mu.
func (m *Manager) writeGosAsync(clusterID string, targets []Node, stage byte) {
	m.authorizeGatesLocked(clusterID, targets, stage)
	m.flushGatesAsync(clusterID, targets)
}

// reconcileGates re-asserts every node's authorized gate ledger on its BMC, so
// a record lost after it was written (a racing writer's SEL wipe, a BMC reset)
// is put back instead of stranding the node waiting on a gate it already has.
// Already-present stages are a no-op read, so this is cheap.
func (m *Manager) reconcileGates(ctx context.Context, clusterID string) {
	m.mu.Lock()
	gate := m.gate
	d := m.deploys[clusterID]
	byHost := m.nodes[clusterID]
	var targets []Node
	sets := map[string][]byte{}
	if gate != nil && d != nil {
		setReadyDone := m.setReady[clusterID].Ready
		for host, nd := range d.Nodes {
			n, ok := byHost[host]
			if !ok || !gatesAtRisk(nd, setReadyDone) {
				continue
			}
			targets = append(targets, n)
			sets[host] = gateBytes(nd.Gates)
		}
	}
	m.mu.Unlock()

	for _, n := range targets {
		wctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err := gate.WriteGate(wctx, n, sets[n.Hostname]...)
		m.ackGates(clusterID, n.Hostname, sets[n.Hostname], err)
		if err != nil {
			log.Printf("gate recheck stages=%v %s: %v", sets[n.Hostname], n.Hostname, err)
		}
		cancel()
	}
}

// gatesAtRisk reports whether a node still has a gate worth re-verifying, so a
// finished deploy stops touching BMCs. A gate is at risk while the node has yet
// to pass the phase it opens, while a write hasn't been confirmed, or — for
// set-ready, authorized only once every node is done — until the master reports
// the cluster finalized.
func gatesAtRisk(nd *NodeDeploy, setReadyDone bool) bool {
	if len(nd.Gates) == 0 || nd.State == StateError {
		return false
	}
	if nd.State != StateDone {
		return true
	}
	for _, g := range nd.Gates {
		if !slices.Contains(nd.GateAck, g) {
			return true
		}
	}
	return slices.Contains(nd.Gates, int(gateStageSetReady)) && !setReadyDone
}

// ackGates records whether the node's authorized gates are confirmed readable
// in its SEL. WriteGate only returns nil once it has read every stage back.
func (m *Manager) ackGates(clusterID, hostname string, stages []byte, err error) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		if err != nil {
			// Keep confirmed stages: a failed re-write does not unwrite a
			// landed record. A stage never confirmed is absent anyway.
			return
		}
		nd.GateAck = make([]int, 0, len(stages))
		for _, s := range stages {
			nd.GateAck = append(nd.GateAck, int(s))
		}
	})
}

// runGateReconciler re-checks the deploy's authorized gates until the deploy
// ends. Gates are the only thing standing between a node and its next phase, so
// a lost record must not be permanent.
func (m *Manager) runGateReconciler(ctx context.Context, clusterID string) {
	t := time.NewTicker(m.cfg.GateRecheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.reconcileGates(ctx, clusterID)
		}
	}
}

// gateTargetsLocked resolves the deploy's nodes for a gate write. which is
// "all", "master", or "peers". Caller must hold m.mu.
func (m *Manager) gateTargetsLocked(clusterID, which string) []Node {
	byHost := m.nodes[clusterID]
	d := m.deploys[clusterID]
	if byHost == nil || d == nil {
		return nil
	}
	var out []Node
	for host, n := range byHost {
		switch which {
		case "all":
			out = append(out, n)
		case "master":
			if host == d.Master {
				out = append(out, n)
			}
		case "peers":
			if host != d.Master {
				out = append(out, n)
			}
		}
	}
	return out
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

// OptOutRepair reports whether this deploy opted out of cluster repair (hidden,
// driver-API only). The agent uses it to drop/clear the persistent opt-out marker.
func (m *Manager) OptOutRepair(clusterID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.deploys[clusterID]
	return d != nil && d.OptOutRepair
}

// SimulateAirgap reports whether this deploy opted into air-gap simulation
// (hidden, driver-API only). The agent uses it to apply the CUBE_AIRGAP block.
func (m *Manager) SimulateAirgap(clusterID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.deploys[clusterID]
	return d != nil && d.SimulateAirgap
}

// Applied records that a node finished applying its (local) snapshot, and
// releases the peers when it is the master — per the deploy record, never the
// agent's claim (its IS_MASTER env can be lost).
func (m *Manager) Applied(clusterID, hostname string) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) { nd.State = StateDone })
	defer m.maybeAutoSetReady(clusterID) // auto mode: trigger set_ready once all done
	m.releasePeersOnMasterDone(clusterID, hostname)
}

// releasePeersOnMasterDone writes the apply "go" to every non-master once the
// master is done — the master-first handoff. Both the in-band applied report
// and the OOB SEL merge call it; re-authorizing a granted stage is a no-op.
func (m *Manager) releasePeersOnMasterDone(clusterID, hostname string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.deploys[clusterID]
	if d == nil || d.Master != hostname {
		return
	}
	// A manual deploy holds the peers for the operator's apply-rest step, so
	// honor the deploy's own Manual flag as well as the global gate.
	if m.manualGate || d.Manual {
		log.Printf("master %s applied; manual mode — peers held for operator (apply-rest)", hostname)
		return
	}
	var targets []Node
	for host, n := range m.nodes[clusterID] {
		if host != d.Master {
			targets = append(targets, n)
		}
	}
	m.writeGosAsync(clusterID, targets, gateStageApply)
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// Start begins (or restarts) a deploy of the given nodes for a cluster.
// master is the hostname whose FTS must finish before other nodes apply.
func (m *Manager) Start(clusterID string, nodes []Node, master string, verifyTargets []string, manual bool, image string, optOutRepair, simulateAirgap bool) (*Deploy, error) {
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
	d := &Deploy{ClusterID: clusterID, StartedAt: nowUTC(), Master: master, VerifyTargets: verifyTargets, Manual: manual, ManualStep: step, OptOutRepair: optOutRepair, SimulateAirgap: simulateAirgap, Nodes: map[string]*NodeDeploy{}}
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
	// Fresh deploy: clear any prior run's set_ready RESULT. SetReadyDone mirrors
	// the persisted set-ready Ready flag on read, so leaving it true shows a
	// stale-green final step on the new run. Keep the operator's input params.
	if in, ok := m.store.LoadSetReady(clusterID); ok && in.Ready {
		in.Ready = false
		in.Message = ""
		m.setReady[clusterID] = in
		if err := m.store.SaveSetReady(clusterID, in); err != nil {
			log.Printf("orchestrator: reset set-ready %s: %v", clusterID, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancels[clusterID] = cancel
	m.persistLocked(d)
	m.mu.Unlock()

	// Wipe each node's SEL so a prior deploy's gate/status records can't replay
	// into this run — gates are authoritative over SEL, so a stale "go" would be
	// dangerous. Then capture the post-clear SEL anchor (last record handle) so
	// the OOB observer floors freshness on the log itself, not the BMC clock.
	// Synchronous + bounded so it completes before runNode re-stamps the
	// driver-endpoint record. Best-effort per node.
	if m.gate != nil || m.sel != nil {
		for _, n := range nodes {
			cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
			if m.gate != nil {
				if err := m.gate.ClearSEL(cctx, n); err != nil {
					log.Printf("deploy start: clear SEL %s: %v", n.Hostname, err)
				}
			}
			// Anchor AFTER the clear: on a clean wipe the log is empty (anchor 0
			// = consider all); if the clear failed, the anchor is the last stale
			// record, so this run's records (written next) still sort after it.
			if m.sel != nil {
				id, err := m.sel.Anchor(cctx, n)
				if err != nil {
					log.Printf("deploy start: SEL anchor %s: %v", n.Hostname, err)
				}
				m.mu.Lock()
				if d2 := m.deploys[clusterID]; d2 != nil {
					if nd := d2.Nodes[n.Hostname]; nd != nil {
						nd.SELAnchor = id
					}
					m.persistLocked(d2)
				}
				m.mu.Unlock()
			}
			ccancel()
		}
	}
	if m.gate != nil {
		// Auto mode never holds: authorize restore + reboot for all nodes and
		// apply for the master up front (peers' apply go is written on master-done,
		// see Applied). Manual mode writes each go on the operator's AdvanceStep.
		if !manual {
			var all, masterN []Node
			for _, n := range nodes {
				all = append(all, n)
				if n.Hostname == master {
					masterN = append(masterN, n)
				}
			}
			// One write per BMC carrying every stage: three concurrent
			// single-stage writers used to contend on one BMC and wipe each
			// other's records, costing the node its restore go.
			m.mu.Lock()
			m.authorizeGatesLocked(clusterID, all, gateStageRestore)
			m.authorizeGatesLocked(clusterID, all, gateStageReboot)
			m.authorizeGatesLocked(clusterID, masterN, gateStageApply)
			m.flushGatesAsync(clusterID, all)
			m.mu.Unlock()
		}
	}

	// Repoint the PXE default to the picked image before powering nodes; abort
	// the run if the shared default is locked by another deploy.
	// Deploy arms autoinstall (unattended restore) + driver_server= (phone home)
	// on the booting entry; SEL still overrides routing per node. Stripped on
	// restore, so the shared entry is armed only while this deploy boots.
	deployArm := "autoinstall"
	if url := m.driverURL(); url != "" {
		deployArm += " driver_server=" + url
	}
	if err := m.flipForBoot(ctx, image, deployArm, func() bool { return m.deployNodesBooted(clusterID) }); err != nil {
		cancel()
		// Clean up only if this deploy is still the registered one — a concurrent
		// Start for the same cluster may have taken over (and won the lock) in the
		// meantime; deleting then would nuke its valid record. The deploy was
		// persisted above before this lock check, so also remove it from disk
		// (under the lock, atomic with the in-memory check) to avoid a ghost
		// "pending" job when the abort is e.g. the shared PXE default being busy.
		m.mu.Lock()
		if m.deploys[clusterID] == d {
			delete(m.deploys, clusterID)
			delete(m.cancels, clusterID)
			_ = m.store.Delete(clusterID)
		}
		m.mu.Unlock()
		return nil, err
	}

	for i, n := range nodes {
		m.wg.Add(1)
		go func(idx int, n Node) {
			defer m.wg.Done()
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
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.pollSEL(ctx, clusterID, nodes)
		}()
	}
	if m.gate != nil {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.runGateReconciler(ctx, clusterID)
		}()
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
	// Reflect the stop in the node states so the UI shows it (cancelling the
	// context alone leaves the last states in place). Non-terminal nodes go to
	// error/cancelled; the PXE-restore watcher then fires (booted() is true once
	// every node is terminal).
	if d := m.deploys[clusterID]; d != nil {
		for _, nd := range d.Nodes {
			if nd.State != StateDone && nd.State != StateError {
				nd.State = StateError
				nd.ErrCode = ErrCancelled
				nd.Message = "deploy cancelled by operator"
				nd.UpdatedAt = nowUTC()
			}
		}
		m.persistLocked(d)
	}
}

// StopAll cancels every running deploy and waits (bounded) for their background
// goroutines to exit, so nothing writes the deploy store afterward. For graceful
// shutdown and test teardown — without it, a deploy's goroutines can still write
// files while the caller removes the data dir.
func (m *Manager) StopAll() {
	m.mu.Lock()
	for id, cancel := range m.cancels {
		cancel()
		delete(m.cancels, id)
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second): // don't hang teardown on a stuck goroutine
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
	// Reloaded from disk (e.g. after a driver restart): the derived fields
	// (progress/phase/lights) are computed, not persisted, so recompute them
	// too — otherwise the UI drops to the legacy 2-light fallback.
	d, err := m.store.Load(clusterID)
	if err != nil {
		return nil, err
	}
	// SetReadyDone is a mirror of the persisted set-ready result; recompute it
	// so a reload (or a deploy marked by a prior process) still greens the final
	// step.
	if in, ok := m.store.LoadSetReady(clusterID); ok {
		d.SetReadyDone = in.Ready
	}
	deriveNodeFields(d)
	m.deriveDeployFields(d)
	return d, nil
}

// deriveNodeFields recomputes each node's presentation-only fields from its
// persisted state.
func deriveNodeFields(d *Deploy) {
	// set-ready cell: active once every node has applied (set_ready is runnable/
	// running) and green only when the master reports set_ready finished.
	allDone := len(d.Nodes) > 0
	for _, nd := range d.Nodes {
		if nd.State != StateDone {
			allDone = false
			break
		}
	}
	setReadyActive := allDone && !d.SetReadyDone
	for host, nd := range d.Nodes {
		nd.Light1, nd.Light2 = nd.lights()
		nd.Phase = nd.phase()
		nd.Progress = nd.progress(host == d.Master, setReadyActive, d.SetReadyDone)
	}
}

// deriveDeployFields recomputes the deploy-level presentation fields (the
// manual Next gate). Must run under the manager lock.
func (m *Manager) deriveDeployFields(d *Deploy) {
	d.CanAdvance = m.canAdvance(d)
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
	deriveNodeFields(&cp)
	m.deriveDeployFields(&cp)
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
	// Ignore SEL records older than this deploy: a previous run's terminal record
	// would otherwise replay into a fresh deploy and complete it instantly. The
	// floor is each node's per-BMC SEL anchor (a record handle captured at deploy
	// start), NOT a timestamp — BMC RTCs are routinely hours off UTC, and a
	// wall-clock floor silently discarded every genuine record on a skewed BMC.
	anchor := map[string]uint16{}
	m.mu.Lock()
	if d := m.deploys[clusterID]; d != nil {
		for host, nd := range d.Nodes {
			anchor[host] = nd.SELAnchor
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
			s, err := m.sel.Observe(ctx, n, anchor[n.Hostname])
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
	m.stampEndpoint(ctx, n) // tell the node which driver booted it
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
		// Advance from anywhere in the preflight-passed → restoring window. The
		// node gates on the restore go via its BMC SEL, so it can reach restore
		// (and finish it) without a second greenlight HTTP round-trip — which
		// leaves the driver's state at PreflightOK. Guarding only on StateRestoring
		// then dropped this report and stranded the node "waiting for the restore
		// gate" even though it had already restored. Never regress a node the
		// agent already pushed past reboot.
		if r := stateRank(nd.State); r >= stateRank(StatePreflightOK) && r < stateRank(StateRebooting) {
			nd.State = StateRebooting
			nd.Message = ""
		}
	})
	// Point the freshly-imaged node at its disk so the post-restore reboot boots
	// the installed OS, not a re-PXE — the install boot armed a persistent
	// Force-PXE (needed by MegaRAC). Best-effort, off the request path.
	m.mu.Lock()
	node, ok := Node{}, false
	if byHost := m.nodes[clusterID]; byHost != nil {
		node, ok = byHost[hostname]
	}
	m.mu.Unlock()
	if ok {
		go func() {
			// Re-assert Force-HDD periodically until the node boots the OS (its
			// agent checks in) or we give up. A single set isn't enough on
			// MegaRAC: the boot-flag "valid" bit expires (~60s) and a slow POST
			// that hasn't consumed it yet falls back to the PXE-first default and
			// re-installs (seen live on the master). Re-stamping keeps it fresh
			// across the ~4-min POST.
			deadline := time.Now().Add(8 * time.Minute)
			for {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				if err := m.exec.SetBootDisk(ctx, node); err != nil {
					log.Printf("orchestrator: set boot-to-disk for %s: %v", hostname, err)
				}
				cancel()
				// Stop once the node is past reboot (OS agent reported in) or on
				// timeout — re-stamping a booted node is pointless.
				if stateRank(m.state(clusterID, hostname)) >= stateRank(StateWaiting) || time.Now().After(deadline) {
					return
				}
				time.Sleep(20 * time.Second)
			}
		}()
	}
}

// ApplyStarted marks the OS-phase agent up and applying: the reboot completed
// and the snapshot apply is in progress. Moves the node off "rebooting" so the
// UI flips reboot → done and apply → active during the long FTS apply.
func (m *Manager) ApplyStarted(clusterID, hostname string, proceed bool) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		switch nd.State {
		case StateRebooting, StateWaiting, StateCheckedIn:
			if proceed {
				nd.State = StateApplying
			} else {
				// OS-phase agent is up and holding for the operator to authorize
				// the apply (manual mode): reboot is done — cell green — with the
				// apply pending, not "applying".
				nd.State = StateWaiting
			}
		}
	})
}

// WaitForMaster marks a rebooted non-master as holding for the master's apply
// (green light 2) — so the UI shows "wait for master", not "applying", until the
// master's SEL 'go' arrives.
func (m *Manager) WaitForMaster(clusterID, hostname string) {
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		if nd.State == StateRebooting || nd.State == StateCheckedIn {
			nd.State = StateWaiting
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
			// The agent gates on the restore record in its own SEL, not on this
			// HTTP answer. Claiming "restoring" before the record is confirmed
			// readable hides a node that is really still parked in preflight.
			// With no gate writer there is no SEL gate: the agent falls back to
			// this greenlight, so the node advances as before.
			if m.gate == nil || slices.Contains(nd.GateAck, int(gateStageRestore)) {
				nd.State = StateRestoring
				nd.Message = ""
			} else {
				nd.State = StatePreflightOK
				nd.Message = "preflight passed — waiting for the restore gate on the BMC"
			}
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
// the new step. Refuses to advance until the current step's nodes have reached
// its state, so the operator can't click straight through the gates.
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
	if d.ManualStep >= StepSetReady {
		return d.ManualStep, nil
	}
	if !m.canAdvance(d) {
		return d.ManualStep, fmt.Errorf("step %d not complete yet — all nodes must reach it first", d.ManualStep)
	}
	d.ManualStep++
	m.persistLocked(d)
	// Authorize the newly-entered step by writing its gate/go SEL to the right
	// BMCs (OOB). This is the authoritative signal — the agents gate on SEL, not
	// the network. Master-first is preserved: peers' apply go is only written at
	// apply-rest, after the master finished (canAdvance for apply-rest requires
	// the master done).
	switch d.ManualStep {
	case StepRestore:
		m.writeGosAsync(clusterID, m.gateTargetsLocked(clusterID, "all"), gateStageRestore)
	case StepReboot:
		m.writeGosAsync(clusterID, m.gateTargetsLocked(clusterID, "all"), gateStageReboot)
	case StepApplyMaster:
		m.writeGosAsync(clusterID, m.gateTargetsLocked(clusterID, "master"), gateStageApply)
	case StepApplyRest:
		m.writeGosAsync(clusterID, m.gateTargetsLocked(clusterID, "peers"), gateStageApply)
	case StepSetReady:
		m.writeGosAsync(clusterID, m.gateTargetsLocked(clusterID, "master"), gateStageSetReady)
	}
	return d.ManualStep, nil
}

// canAdvance reports whether the current manual step's precondition is met: the
// nodes have reached the state this step gates. Errored nodes (rank 0) block —
// the operator rekicks or cancels rather than advancing past a failure.
func (m *Manager) canAdvance(d *Deploy) bool {
	if !d.Manual || d.ManualStep >= StepSetReady {
		return false
	}
	switch d.ManualStep {
	case StepPreflight: // -> restore: every node passed preflight
		return m.fabricReadyLocked(d)
	case StepRestore: // -> reboot: every node finished restoring
		return allReached(d, 4) // >= rebooting
	case StepReboot: // -> apply-master: every node rebooted + checked in
		return allReached(d, 5) // >= checked-in/waiting
	case StepApplyMaster: // -> apply-rest: the master finished applying
		nd := d.Nodes[d.Master]
		return d.Master == "" || (nd != nil && nd.State == StateDone)
	case StepApplyRest: // -> set-ready: every node finished applying
		return allReached(d, 7) // all done
	}
	return false
}

// allReached reports whether every node's pipeline rank is >= want.
func allReached(d *Deploy, want int) bool {
	for _, nd := range d.Nodes {
		if pipelineRank(nd.State) < want {
			return false
		}
	}
	return true
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
