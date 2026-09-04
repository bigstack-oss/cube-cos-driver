package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	goipmi "github.com/bougou/go-ipmi"
)

// ccErr carries an IPMI completion code like *goipmi.ResponseError, whose
// fields are unexported so a test can't build one.
type ccErr struct{ cc goipmi.CompletionCode }

func (e ccErr) Error() string                         { return fmt.Sprintf("cc %#02x", uint8(e.cc)) }
func (e ccErr) CompletionCode() goipmi.CompletionCode { return e.cc }

// Wiping the SEL is only ever a fix for "out of space" (0xC4). Clearing on any
// other error destroys the gate records other writers already landed — that is
// how a node loses its restore go and parks in preflight forever.
func TestSELWipeOnlyOnOutOfSpace(t *testing.T) {
	if !selFull(ccErr{goipmi.CompletionCodeOutOfSpace}) {
		t.Error("0xC4 out-of-space must authorize a SEL clear")
	}
	for _, err := range []error{
		nil,
		errors.New("read udp 10.32.10.41:623: i/o timeout"),
		ccErr{goipmi.CompletionCodeNodeBusy},
		ccErr{goipmi.CompletionCodeProcessTimeout},
		fmt.Errorf("add SEL entry: %w", ccErr{goipmi.CompletionCodeNodeBusy}),
	} {
		if selFull(err) {
			t.Errorf("%v must not authorize a SEL clear", err)
		}
	}
}

// fakeBMC models one node's SEL well enough to test the driver's gate
// bookkeeping: stages land in it, a test can wipe them the way the driver's own
// error recovery did, and it records call overlap so a fan-out is visible.
type fakeBMC struct {
	mu       sync.Mutex
	gates    map[string]map[byte]bool // hostname -> stage -> present
	calls    map[string][][]byte      // hostname -> stage sets, per WriteGate call
	inFlight map[string]int
	overlap  bool
	// drop names stages the BMC silently refuses to store, standing in for a
	// record that never lands (or is wiped) without the write reporting it.
	drop map[byte]bool
}

func newFakeBMC() *fakeBMC {
	return &fakeBMC{
		gates:    map[string]map[byte]bool{},
		calls:    map[string][][]byte{},
		inFlight: map[string]int{},
	}
}

func (f *fakeBMC) WriteGate(_ context.Context, n Node, stages ...byte) error {
	f.mu.Lock()
	f.inFlight[n.Hostname]++
	if f.inFlight[n.Hostname] > 1 {
		f.overlap = true
	}
	set := append([]byte(nil), stages...)
	f.calls[n.Hostname] = append(f.calls[n.Hostname], set)
	f.mu.Unlock()

	time.Sleep(2 * time.Millisecond) // widen the window a fan-out would race in

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gates[n.Hostname] == nil {
		f.gates[n.Hostname] = map[byte]bool{}
	}
	missing := false
	for _, s := range stages {
		if f.drop[s] {
			missing = true
			continue
		}
		f.gates[n.Hostname][s] = true
	}
	f.inFlight[n.Hostname]--
	if missing {
		return fmt.Errorf("gate stages not present in SEL after write")
	}
	return nil
}

func (f *fakeBMC) ClearSEL(_ context.Context, n Node) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.gates, n.Hostname)
	return nil
}

// wipe drops one stage, standing in for a SEL clear by a racing writer.
func (f *fakeBMC) wipe(host string, stage byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.gates[host], stage)
}

func (f *fakeBMC) has(host string, stage byte) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gates[host][stage]
}

// callsFor returns the stage sets each WriteGate call carried for a host.
func (f *fakeBMC) callsFor(host string) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.calls[host]...)
}

func (f *fakeBMC) sawOverlap() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.overlap
}

// waitGate polls until the stage is present on the fake BMC.
func waitGate(t *testing.T, f *fakeBMC, host string, stage byte, why string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f.has(host, stage) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("gate stage=%d never appeared on %s: %s", stage, host, why)
}

// An auto deploy authorizes restore, reboot and apply. They must reach one
// node's BMC without overlapping calls — the concurrent per-stage fan-out is
// what let one writer's error recovery wipe another's landed record.
func TestAutoDeployWritesGatesWithoutOverlap(t *testing.T) {
	m := newSettleManager(t)
	f := newFakeBMC()
	m.SetGateWriter(f)
	if _, err := m.Start("cm", []Node{{Hostname: "cc1", MachineID: "1"}}, "cc1", nil, false, "", false, false); err != nil {
		t.Fatal(err)
	}
	waitGate(t, f, "cc1", gateStageRestore, "auto mode authorizes restore up front")
	waitGate(t, f, "cc1", gateStageReboot, "auto mode authorizes reboot up front")
	waitGate(t, f, "cc1", gateStageApply, "auto mode authorizes the master's apply up front")
	if f.sawOverlap() {
		t.Error("gate writes overlapped on one BMC — concurrent sessions can wipe each other's records")
	}
	// One call carrying the whole set, not one call per stage.
	calls := f.callsFor("cc1")
	if len(calls) != 1 {
		t.Fatalf("want 1 gate write for cc1, got %d: %v", len(calls), calls)
	}
	if len(calls[0]) != 3 {
		t.Errorf("the single write must carry all three stages, got %v", calls[0])
	}
}

// The restore gate is written once and then never re-checked, so anything that
// clears the SEL afterwards (a racing writer's recovery, a BMC reset) strands
// the node in its preflight loop with no way out. The driver must notice the
// record is gone and put it back.
func TestGateReconcilerRestoresWipedGate(t *testing.T) {
	m := newSettleManager(t)
	m.cfg.GateRecheck = 10 * time.Millisecond
	f := newFakeBMC()
	m.SetGateWriter(f)
	if _, err := m.Start("cm", []Node{{Hostname: "cc1", MachineID: "1"}}, "cc1", nil, false, "", false, false); err != nil {
		t.Fatal(err)
	}
	waitGate(t, f, "cc1", gateStageRestore, "auto mode authorizes restore up front")

	f.wipe("cc1", gateStageRestore)
	if f.has("cc1", gateStageRestore) {
		t.Fatal("wipe did not take")
	}
	waitGate(t, f, "cc1", gateStageRestore, "an authorized gate that vanished must be re-asserted")
}

// The agent gates on the restore record in its own SEL, so a node whose BMC
// never took that record is still parked in preflight however green its report
// is. Showing "restoring" there points diagnosis at the wrong phase.
func TestNodeNotRestoringUntilRestoreGateIsReadable(t *testing.T) {
	m := newSettleManager(t)
	m.cfg.GateRecheck = time.Hour // no reconciler rewrite during the test
	f := newFakeBMC()
	f.drop = map[byte]bool{gateStageRestore: true} // BMC swallows the restore go
	m.SetGateWriter(f)
	if _, err := m.Start("cm", []Node{{Hostname: "cc1", MachineID: "1"}}, "cc1", nil, false, "", false, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "cm", "cc1", StateImaged)

	m.PreflightReport("cm", "cc1", NodePreflight{CarrierOK: true, Passed: true})
	if !m.GreenLight1("cm", "cc1") {
		t.Fatal("green light 1 should clear once the only node passes preflight")
	}
	d, err := m.Status("cm")
	if err != nil {
		t.Fatal(err)
	}
	nd := d.Nodes["cc1"]
	if nd.State == StateRestoring {
		t.Error("node reported restoring while its restore gate is not readable on the BMC")
	}
	if !strings.Contains(nd.Message, "restore gate") {
		t.Errorf("message should name the missing restore gate, got %q", nd.Message)
	}
}

// The reconciler exists to protect gates a node still has to pass. Once a node
// is done with every gate confirmed, it must stop touching that BMC — a driver
// that keeps polling every node of every past deploy never goes quiet.
func TestGateReconcilerStopsOnceNodeIsDone(t *testing.T) {
	nd := &NodeDeploy{State: StateRestoring, Gates: []int{1, 2, 3}, GateAck: []int{1, 2, 3}}
	if !gatesAtRisk(nd, false) {
		t.Error("a node still mid-install must keep its gates verified")
	}
	nd.State = StateDone
	if gatesAtRisk(nd, false) {
		t.Error("a done node with every gate confirmed needs no further BMC reads")
	}
	nd.GateAck = []int{1, 2}
	if !gatesAtRisk(nd, false) {
		t.Error("an unconfirmed gate must keep being retried")
	}
	// set-ready is authorized only after every node is done, so it outlives the
	// node's own phases — keep verifying it until the master finalizes.
	nd.Gates, nd.GateAck = []int{1, 2, 3, 4}, []int{1, 2, 3, 4}
	if !gatesAtRisk(nd, false) {
		t.Error("the set-ready gate must stay verified until the cluster is finalized")
	}
	if gatesAtRisk(nd, true) {
		t.Error("once set_ready is done there is nothing left to verify")
	}
	nd.State = StateError
	if gatesAtRisk(nd, false) {
		t.Error("an errored node has no gate to pass")
	}
}

// A manual deploy must not have later stages pre-authorized, and re-asserting
// an earlier gate must never smuggle in one the operator hasn't reached.
func TestManualDeployDoesNotPreAuthorizeLaterGates(t *testing.T) {
	m := newSettleManager(t)
	m.cfg.GateRecheck = 10 * time.Millisecond
	f := newFakeBMC()
	m.SetGateWriter(f)
	if _, err := m.Start("cm", []Node{{Hostname: "cc1", MachineID: "1"}}, "cc1", nil, true, "", false, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let any writes and rechecks happen
	for _, s := range []byte{gateStageRestore, gateStageReboot, gateStageApply, gateStageSetReady} {
		if f.has("cc1", s) {
			t.Errorf("manual deploy pre-authorized gate stage=%d before the operator advanced", s)
		}
	}
}

// The master's in-band "applied" POST is routinely lost — the snapshot apply
// moves it onto the mgmt network, off the flat L2 the driver lives on — and the
// driver then learns it finished only from its BMC. If that path doesn't carry
// the master-first handoff, every peer waits on an apply go nobody writes.
func TestMasterDoneOutOfBandReleasesPeers(t *testing.T) {
	m := newSettleManager(t)
	f := newFakeBMC()
	m.SetGateWriter(f)
	nodes := []Node{{Hostname: "m", MachineID: "1"}, {Hostname: "p", MachineID: "2"}}
	if _, err := m.Start("cm", nodes, "m", nil, false, "", false, false); err != nil {
		t.Fatal(err)
	}
	waitGate(t, f, "p", gateStageReboot, "auto mode authorizes the peer's reboot up front")
	if f.has("p", gateStageApply) {
		t.Fatal("peer apply must stay unauthorized until the master is done")
	}

	// Master reaches done OUT OF BAND only — no Applied() call, as when its
	// in-band report never arrives.
	m.MergeSEL("cm", "m", SELStatus{Phase: "applied", Result: "ok"})

	waitGate(t, f, "p", gateStageApply, "master done OOB must release the peers")
}

// A manual run hands the peers to the operator at apply-rest, so the OOB path
// must not release them either.
func TestMasterDoneOutOfBandHoldsPeersWhenManual(t *testing.T) {
	m := newSettleManager(t)
	f := newFakeBMC()
	m.SetGateWriter(f)
	nodes := []Node{{Hostname: "m", MachineID: "1"}, {Hostname: "p", MachineID: "2"}}
	if _, err := m.Start("cm", nodes, "m", nil, true, "", false, false); err != nil {
		t.Fatal(err)
	}
	m.MergeSEL("cm", "m", SELStatus{Phase: "applied", Result: "ok"})
	time.Sleep(100 * time.Millisecond)
	if f.has("p", gateStageApply) {
		t.Error("manual mode must hold the peers for the operator's apply-rest step")
	}
}

// Who the master is comes from the deploy record. A peer reporting applied —
// including one whose lost IS_MASTER env made it claim the role — must never
// release the fleet.
func TestPeerAppliedNeverReleasesPeers(t *testing.T) {
	m := newSettleManager(t)
	f := newFakeBMC()
	m.SetGateWriter(f)
	nodes := []Node{{Hostname: "m", MachineID: "1"}, {Hostname: "p", MachineID: "2"}}
	if _, err := m.Start("cm", nodes, "m", nil, false, "", false, false); err != nil {
		t.Fatal(err)
	}
	m.Applied("cm", "p")
	time.Sleep(100 * time.Millisecond)
	if f.has("p", gateStageApply) {
		t.Error("a peer reporting applied must not authorize the apply stage")
	}
}

// A gate confirmed readable once does not stop existing because a later
// re-write couldn't reach the BMC. Wiping the ack parks the node on "waiting
// for the restore gate" while it is really restoring.
func TestGateAckSurvivesFailedRewrite(t *testing.T) {
	m := newSettleManager(t)
	f := newFakeBMC()
	m.SetGateWriter(f)
	if _, err := m.Start("cm", []Node{{Hostname: "cc1", MachineID: "1"}}, "cc1", nil, false, "", false, false); err != nil {
		t.Fatal(err)
	}
	waitGate(t, f, "cc1", gateStageRestore, "auto mode authorizes restore up front")

	m.ackGates("cm", "cc1", []byte{gateStageRestore}, errors.New("IPMI LAN: no datagram matched before deadline"))
	d, err := m.Status("cm")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(d.Nodes["cc1"].GateAck, int(gateStageRestore)) {
		t.Error("a failed re-write must not erase an already-confirmed gate ack")
	}
}
