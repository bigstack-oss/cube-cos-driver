package orchestrator

import (
	"os"
	"context"
	"testing"
	"time"
)

// newSettleManager builds a Manager whose store dir is cleaned only after
// background runNode goroutines have exited (used by gate-logic tests that
// don't drive nodes to a terminal state).
func newSettleManager(t *testing.T) *Manager {
	t.Helper()
	dir, err := os.MkdirTemp("", "orch-gate-*")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(store, NewFakeExecutor(), Config{PollInterval: time.Millisecond, StageTimeout: 2 * time.Second})
	t.Cleanup(func() {
		for _, id := range []string{"cm", "ca"} {
			m.Cancel(id)
		}
		time.Sleep(50 * time.Millisecond) // let cancelled goroutines exit
		os.RemoveAll(dir)
	})
	return m
}

func newTestManager(t *testing.T, exec Executor) *Manager {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(store, exec, Config{PollInterval: time.Millisecond, StageTimeout: 2 * time.Second})
}

func waitFor(t *testing.T, m *Manager, cluster, host string, want State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d, err := m.Status(cluster)
		if err == nil {
			if nd := d.Nodes[host]; nd != nil && nd.State == want {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	d, _ := m.Status(cluster)
	got := "<none>"
	if d != nil && d.Nodes[host] != nil {
		got = string(d.Nodes[host].State)
	}
	t.Fatalf("node %s: want state %s, got %s", host, want, got)
}

func TestDeployReachesImagedThenAgentApplies(t *testing.T) {
	m := newTestManager(t, NewFakeExecutor())
	nodes := []Node{
		{Hostname: "cube-1", MachineID: "m1", MACs: []string{"aa:01"}},
		{Hostname: "cube-2", MachineID: "m2", MACs: []string{"aa:02"}},
	}
	if _, err := m.Start("cl1", nodes, "cube-1", nil, false, ""); err != nil {
		t.Fatal(err)
	}

	// Both nodes drive through IPMI + imaging to imaged.
	waitFor(t, m, "cl1", "cube-1", StateImaged)
	waitFor(t, m, "cl1", "cube-2", StateImaged)

	// Both nodes pass installer preflight → green light 1 clears.
	pass := NodePreflight{CarrierOK: true, ClockSkewSec: 0.2, Passed: true}
	m.PreflightReport("cl1", "cube-1", pass)
	m.PreflightReport("cl1", "cube-2", pass)
	if !m.GreenLight1("cl1", "cube-1") || !m.GreenLight1("cl1", "cube-2") {
		t.Fatal("green light 1 should clear once all nodes pass preflight")
	}
	waitFor(t, m, "cl1", "cube-1", StateRestoring)

	// After reboot, cube-1 (master) checks in and applies.
	if hold := m.CheckIn("cl1", "cube-1"); hold {
		t.Fatal("master must not hold at green light 2")
	}
	waitFor(t, m, "cl1", "cube-1", StateCheckedIn)
	m.Report("cl1", "cube-1", StateApplying, "", nil)
	m.Report("cl1", "cube-1", StateDone, "applied", nil)
	waitFor(t, m, "cl1", "cube-1", StateDone)

	d, _ := m.Status("cl1")
	if d.Nodes["cube-1"].InstallerPreflight == nil || !d.Nodes["cube-1"].InstallerPreflight.Passed {
		t.Fatalf("installer preflight not recorded: %+v", d.Nodes["cube-1"].InstallerPreflight)
	}
	if d.Nodes["cube-1"].Light2 != LightGreen {
		t.Fatalf("light2 = %q, want green", d.Nodes["cube-1"].Light2)
	}
}

func TestGreenLight1Barrier(t *testing.T) {
	m := newTestManager(t, NewFakeExecutor())
	m.Start("cl1", []Node{{Hostname: "a", MachineID: "m1"}, {Hostname: "b", MachineID: "m2"}}, "a", nil, false, "")
	waitFor(t, m, "cl1", "a", StateImaged)
	waitFor(t, m, "cl1", "b", StateImaged)

	// Only one node passed → GL1 withheld.
	m.PreflightReport("cl1", "a", NodePreflight{CarrierOK: true, Passed: true})
	if m.GreenLight1("cl1", "a") {
		t.Fatal("GL1 must be withheld until every node passes")
	}

	// b reports a degraded bond (carrier down) → still withheld, PF_CARRIER.
	m.PreflightReport("cl1", "b", NodePreflight{CarrierOK: false, Passed: false, Matrix: []PreflightResult{{Target: "bond0", OK: false}}})
	if m.GreenLight1("cl1", "a") {
		t.Fatal("GL1 must be withheld while a node is degraded")
	}
	d, _ := m.Status("cl1")
	if d.Nodes["b"].ErrCode != ErrPFCarrier {
		t.Fatalf("b errCode = %q, want %s", d.Nodes["b"].ErrCode, ErrPFCarrier)
	}
	if d.Nodes["b"].Light1 != LightYellow {
		t.Fatalf("b light1 = %q, want yellow (still converging)", d.Nodes["b"].Light1)
	}

	// b re-kicks and passes but with excessive skew → withheld, PF_CLOCK_SKEW.
	m.PreflightReport("cl1", "b", NodePreflight{CarrierOK: true, ClockSkewSec: 12, Passed: false})
	if m.GreenLight1("cl1", "a") {
		t.Fatal("GL1 must be withheld while a node's clock skew exceeds the gate")
	}
	d, _ = m.Status("cl1")
	if d.Nodes["b"].ErrCode != ErrPFSkew {
		t.Fatalf("b errCode = %q, want %s", d.Nodes["b"].ErrCode, ErrPFSkew)
	}

	// b re-kicks clean → GL1 clears for both; both advance to restoring.
	m.PreflightReport("cl1", "b", NodePreflight{CarrierOK: true, ClockSkewSec: 0.1, Passed: true})
	if !m.GreenLight1("cl1", "b") {
		t.Fatal("GL1 should clear once every node passes")
	}
	waitFor(t, m, "cl1", "b", StateRestoring)
}

func TestGreenLight2MasterFirst(t *testing.T) {
	m := newTestManager(t, NewFakeExecutor())
	m.Start("cl1", []Node{
		{Hostname: "master", MachineID: "m1"},
		{Hostname: "worker", MachineID: "m2"},
	}, "master", nil, false, "")
	waitFor(t, m, "cl1", "master", StateImaged)
	waitFor(t, m, "cl1", "worker", StateImaged)

	// (Preflight barrier already cleared pre-restore; here we test GL2 only.)
	// Worker checks in first → holds until master is done.
	if hold := m.CheckIn("cl1", "worker"); !hold {
		t.Fatal("worker should hold until master is done")
	}
	waitFor(t, m, "cl1", "worker", StateWaiting)

	// Master checks in → no hold; drive it to done.
	if hold := m.CheckIn("cl1", "master"); hold {
		t.Fatal("master must not hold at green light 2")
	}
	m.Report("cl1", "master", StateApplying, "", nil)
	m.Report("cl1", "master", StateDone, "applied", nil)

	// Now the worker clears.
	if hold := m.CheckIn("cl1", "worker"); hold {
		t.Fatal("worker should proceed once master is done")
	}
	waitFor(t, m, "cl1", "worker", StateCheckedIn)
}

type fakeVerifier struct {
	targets []string
	result  []PreflightResult
	ran     bool
}

func (f *fakeVerifier) Verify(_ context.Context, targets []string) []PreflightResult {
	f.targets = targets
	f.ran = true
	return f.result
}

func TestClusterVerifyGatedOnAllDone(t *testing.T) {
	m := newTestManager(t, NewFakeExecutor())
	fv := &fakeVerifier{result: []PreflightResult{{Target: "reach 10.254.0.100", OK: true}}}
	m.SetVerifier(fv)
	m.Start("cl1", []Node{
		{Hostname: "cube-1", MachineID: "m1"},
		{Hostname: "cube-2", MachineID: "m2"},
	}, "cube-1", []string{"10.254.0.100"}, false, "")
	waitFor(t, m, "cl1", "cube-1", StateImaged)
	waitFor(t, m, "cl1", "cube-2", StateImaged)

	// Only one node done → verify must NOT run yet.
	m.Report("cl1", "cube-1", StateDone, "", nil)
	time.Sleep(20 * time.Millisecond)
	if fv.ran {
		t.Fatal("cluster verify ran before all nodes were done")
	}
	d, _ := m.Status("cl1")
	if d.ClusterReady {
		t.Fatal("clusterReady set prematurely")
	}

	// Second node done → verify runs, cluster becomes ready.
	m.Report("cl1", "cube-2", StateDone, "", nil)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d, _ := m.Status("cl1"); d.ClusterReady {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	d, _ = m.Status("cl1")
	if !d.ClusterReady || len(d.Verify) != 1 {
		t.Fatalf("cluster not verified after all done: %+v", d)
	}
	if len(fv.targets) != 1 || fv.targets[0] != "10.254.0.100" {
		t.Fatalf("verify targets = %v", fv.targets)
	}
}

func TestPreflightFailureIsolatesNode(t *testing.T) {
	exec := NewFakeExecutor()
	exec.FailPreflight["bad"] = true
	m := newTestManager(t, exec)
	m.Start("cl1", []Node{
		{Hostname: "bad", MachineID: "m1"},
		{Hostname: "good", MachineID: "m2"},
	}, "good", nil, false, "")
	waitFor(t, m, "cl1", "bad", StateError)
	waitFor(t, m, "cl1", "good", StateImaged) // unaffected
	d, _ := m.Status("cl1")
	if d.Nodes["bad"].Message == "" {
		t.Fatal("expected error message on failed node")
	}
}

func TestStatusPersists(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	m := NewManager(store, NewFakeExecutor(), Config{PollInterval: time.Millisecond, StageTimeout: time.Second})
	m.Start("cl9", []Node{{Hostname: "n1", MachineID: "m1"}}, "n1", nil, false, "")
	waitFor(t, m, "cl9", "n1", StateImaged)
	// A fresh manager backed by the same store reads persisted status.
	m2 := NewManager(store, NewFakeExecutor(), Config{})
	d, err := m2.Status("cl9")
	if err != nil || d.Nodes["n1"] == nil {
		t.Fatalf("persisted status not loaded: %v", err)
	}
}

func TestCancelStopsStepping(t *testing.T) {
	// A slow executor (many observe steps) so we can cancel mid-flight.
	exec := NewFakeExecutor()
	exec.StepsToDone = 100000
	m := newTestManager(t, exec)
	m.Start("clc", []Node{{Hostname: "n1", MachineID: "m1"}}, "n1", nil, false, "")
	waitFor(t, m, "clc", "n1", StateNetbooting)
	m.Cancel("clc")
	// State should stop advancing to imaged.
	time.Sleep(20 * time.Millisecond)
	d, _ := m.Status("clc")
	if d.Nodes["n1"].State == StateImaged || d.Nodes["n1"].State == StateDone {
		t.Fatalf("cancel did not stop stepping: %s", d.Nodes["n1"].State)
	}
}

// Manual mode gates each phase on the operator's step cursor.
func TestManualStepGates(t *testing.T) {
	m := newSettleManager(t)
	nodes := []Node{{Hostname: "m", MachineID: "1"}, {Hostname: "p", MachineID: "2"}}
	if _, err := m.Start("cm", nodes, "m", nil, true, ""); err != nil {
		t.Fatal(err)
	}
	pass := NodePreflight{CarrierOK: true, Passed: true}
	m.PreflightReport("cm", "m", pass)
	m.PreflightReport("cm", "p", pass)

	// Step 1 (preflight): GL1 must NOT clear despite fabric ready.
	if m.GreenLight1("cm", "m") {
		t.Fatal("GL1 cleared at preflight step — should hold for operator")
	}
	// Reboot + master apply gates also closed.
	if m.RebootProceed("cm") {
		t.Fatal("reboot proceeded at preflight step")
	}
	if m.ApplyProceed("cm", "m") {
		t.Fatal("master apply proceeded at preflight step")
	}

	// Step 1 completes (fabric ready), so Next → restore authorized; GL1 clears.
	if s, err := m.AdvanceStep("cm"); err != nil || s != StepRestore {
		t.Fatalf("advance to restore: step=%d err=%v", s, err)
	}
	if !m.GreenLight1("cm", "m") || !m.GreenLight1("cm", "p") {
		t.Fatal("GL1 should clear once restore authorized")
	}
	if m.RebootProceed("cm") {
		t.Fatal("reboot should still hold at restore step")
	}

	// Next must be REFUSED until both nodes actually finish restoring.
	if _, err := m.AdvanceStep("cm"); err == nil {
		t.Fatal("advance to reboot should refuse until nodes reach rebooting")
	}
	m.RestoreDone("cm", "m")
	m.RestoreDone("cm", "p")

	// Now Next → reboot authorized.
	if s, err := m.AdvanceStep("cm"); err != nil || s != StepReboot {
		t.Fatalf("advance to reboot: step=%d err=%v", s, err)
	}
	if !m.RebootProceed("cm") {
		t.Fatal("reboot should proceed once authorized")
	}
	if m.ApplyProceed("cm", "m") {
		t.Fatal("master apply should still hold at reboot step")
	}

	// Refused until both check in post-reboot.
	if _, err := m.AdvanceStep("cm"); err == nil {
		t.Fatal("advance to apply-master should refuse until nodes check in")
	}
	m.CheckIn("cm", "m")
	m.CheckIn("cm", "p")

	// Next → apply-master authorized (master only; peer still held).
	if s, err := m.AdvanceStep("cm"); err != nil || s != StepApplyMaster {
		t.Fatalf("advance to apply-master: step=%d err=%v", s, err)
	}
	if !m.ApplyProceed("cm", "m") {
		t.Fatal("master apply should proceed once authorized")
	}
	if m.ApplyProceed("cm", "p") {
		t.Fatal("peer apply should hold until apply-rest")
	}

	// Refused until the master finishes applying.
	if _, err := m.AdvanceStep("cm"); err == nil {
		t.Fatal("advance to apply-rest should refuse until master is done")
	}
	m.Applied("cm", "m", true) // master → done

	// Next → apply-rest: peer proceeds.
	if s, err := m.AdvanceStep("cm"); err != nil || s != StepApplyRest {
		t.Fatalf("advance to apply-rest: step=%d err=%v", s, err)
	}
	if !m.ApplyProceed("cm", "p") {
		t.Fatal("peer apply should proceed at apply-rest")
	}

	// set_ready is its own step: refused until every node is done.
	if _, err := m.AdvanceStep("cm"); err == nil {
		t.Fatal("advance to set-ready should refuse until all nodes done")
	}
	m.Applied("cm", "p", false) // peer → done

	// Next → set-ready (final); step caps there.
	if s, err := m.AdvanceStep("cm"); err != nil || s != StepSetReady {
		t.Fatalf("advance to set-ready: step=%d err=%v", s, err)
	}
	if s, _ := m.AdvanceStep("cm"); s != StepSetReady {
		t.Fatalf("step should cap at %d, got %d", StepSetReady, s)
	}
}

// Auto mode never gates (all proceed immediately).
func TestAutoModeNoGates(t *testing.T) {
	m := newSettleManager(t)
	m.Start("ca", []Node{{Hostname: "m", MachineID: "1"}}, "m", nil, false, "")
	m.PreflightReport("ca", "m", NodePreflight{CarrierOK: true, Passed: true})
	if !m.GreenLight1("ca", "m") {
		t.Fatal("auto GL1 should clear when fabric ready")
	}
	if !m.RebootProceed("ca") || !m.ApplyProceed("ca", "m") {
		t.Fatal("auto mode must never gate reboot/apply")
	}
	if _, err := m.AdvanceStep("ca"); err == nil {
		t.Fatal("AdvanceStep on an auto deploy should error")
	}
}

// ApplyStarted must reflect the apply authorization: while the OS-phase agent
// holds for the operator (proceed=false) the node shows reboot-done + waiting
// (cell green, apply pending), and flips to applying only when authorized
// (proceed=true). Auto mode always passes proceed=true, so it never lingers in
// waiting — the regression guard for auto deploys.
func TestApplyStartedProceedFlipsState(t *testing.T) {
	m := newSettleManager(t)
	m.Start("cm", []Node{{Hostname: "m", MachineID: "1"}}, "m", nil, true, "")
	// Put the node in the post-reboot state the OS agent reports from.
	m.Report("cm", "m", StateRebooting, "", nil)

	// Holding for the operator: reboot done → Waiting, NOT Applying.
	m.ApplyStarted("cm", "m", false)
	if d, _ := m.Status("cm"); d.Nodes["m"].State != StateWaiting {
		t.Fatalf("proceed=false: state=%s, want %s (reboot green, apply pending)", d.Nodes["m"].State, StateWaiting)
	}
	// Authorized (operator advanced / auto mode): flip to Applying.
	m.ApplyStarted("cm", "m", true)
	if d, _ := m.Status("cm"); d.Nodes["m"].State != StateApplying {
		t.Fatalf("proceed=true: state=%s, want %s", d.Nodes["m"].State, StateApplying)
	}
}

// set_ready must not fire until EVERY node has finished applying. GetSetReady
// withholds Trigger until all deploy nodes reach done.
func TestSetReadyGatesOnAllNodesApplied(t *testing.T) {
	m := newSettleManager(t)
	nodes := []Node{{Hostname: "m", MachineID: "1"}, {Hostname: "p", MachineID: "2"}}
	if _, err := m.Start("cm", nodes, "m", nil, false, ""); err != nil {
		t.Fatal(err)
	}
	m.SubmitSetReady("cm", SetReadyInput{CreateExternal: true, CIDR: "10.0.0.0/16"})

	// Master done, peer not → Trigger withheld.
	m.advance("cm", "m", StateDone)
	if m.GetSetReady("cm").Trigger {
		t.Fatal("set_ready Trigger should be withheld while a node is still applying")
	}
	// All done → Trigger armed.
	m.advance("cm", "p", StateDone)
	if !m.GetSetReady("cm").Trigger {
		t.Fatal("set_ready Trigger should arm once all nodes are done")
	}
	// The stored values still surface (UI pre-fill).
	if m.GetSetReady("cm").CIDR != "10.0.0.0/16" {
		t.Fatal("set_ready values lost")
	}
}

// A deploy served from disk (not in memory, e.g. after a driver restart) must
// still carry the recomputed progress strip — otherwise the UI falls back to
// the legacy 2-light display. Regression for the "only 2 stages" report.
func TestStatusRecomputesProgressOnReload(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Persist a finished deploy with progress unset (as it is on disk —
	// progress is computed, never stored).
	if err := store.Save(&Deploy{
		ClusterID: "cl1",
		Nodes: map[string]*NodeDeploy{
			"n1": {Hostname: "n1", State: StateDone},
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Fresh manager: the deploy is only on disk, so Status() takes the Load path.
	m := NewManager(store, NewFakeExecutor(), Config{})
	d, err := m.Status("cl1")
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Nodes["n1"].Progress; len(got) != 4 {
		t.Fatalf("reloaded deploy should have the 4-cell progress strip, got %v", got)
	}
}

// An inspect boot arms persistent Force-PXE; when the inspect reaches a
// terminal state (reported or error) the boot device must be reset to disk —
// otherwise the node re-PXEs into the installer on its next power-on.
func TestInspectResetsBootDeviceOnTerminal(t *testing.T) {
	exec := NewFakeExecutor()
	m := newTestManager(t, exec)

	nodes := []Node{
		{Hostname: "n1", MachineID: "m1", BMCAddress: "b1"},
		{Hostname: "n2", MachineID: "m2", BMCAddress: "b2"},
	}
	if err := m.StartInspect(nodes, map[string]string{"m1": "cc1", "m2": "cc2"}, ""); err != nil {
		t.Fatal(err)
	}

	m.InspectReported("m1") // agent checked in and reported
	m.expireInspect("m2")   // never checked in — timed out

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(exec.BootDiskNodes()) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := exec.BootDiskNodes()
	if len(got) != 2 || !got["m1"] || !got["m2"] {
		t.Fatalf("SetBootDisk not issued for terminal inspects: %v", got)
	}
}
