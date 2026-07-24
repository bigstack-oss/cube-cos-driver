package orchestrator

import (
	"testing"
	"time"
)

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
	if _, err := m.Start("cl1", nodes, "cube-1"); err != nil {
		t.Fatal(err)
	}

	// Both nodes drive through IPMI + imaging to imaged.
	waitFor(t, m, "cl1", "cube-1", StateImaged)
	waitFor(t, m, "cl1", "cube-2", StateImaged)

	// Agent checks in and applies for cube-1.
	m.CheckIn("cl1", "cube-1")
	waitFor(t, m, "cl1", "cube-1", StateCheckedIn)
	m.Report("cl1", "cube-1", StateNetPreflight, "", []PreflightResult{{Target: "gateway", OK: true}})
	m.Report("cl1", "cube-1", StateApplying, "", nil)
	m.Report("cl1", "cube-1", StateDone, "applied", nil)
	waitFor(t, m, "cl1", "cube-1", StateDone)

	d, _ := m.Status("cl1")
	if len(d.Nodes["cube-1"].Preflight) != 1 || !d.Nodes["cube-1"].Preflight[0].OK {
		t.Fatalf("preflight not recorded: %+v", d.Nodes["cube-1"].Preflight)
	}
}

func TestNonMasterHoldsUntilMasterDone(t *testing.T) {
	m := newTestManager(t, NewFakeExecutor())
	m.Start("cl1", []Node{
		{Hostname: "master", MachineID: "m1"},
		{Hostname: "worker", MachineID: "m2"},
	}, "master")
	waitFor(t, m, "cl1", "master", StateImaged)
	waitFor(t, m, "cl1", "worker", StateImaged)

	// Worker checks in first → must hold (master not done yet).
	if hold := m.CheckIn("cl1", "worker"); !hold {
		t.Fatal("worker should hold until master is done")
	}
	waitFor(t, m, "cl1", "worker", StateWaiting)

	// Master checks in → no hold; drive it to done.
	if hold := m.CheckIn("cl1", "master"); hold {
		t.Fatal("master must not hold")
	}
	m.Report("cl1", "master", StateApplying, "", nil)
	m.Report("cl1", "master", StateDone, "applied", nil)

	// Now the worker's next check-in clears.
	if hold := m.CheckIn("cl1", "worker"); hold {
		t.Fatal("worker should proceed once master is done")
	}
	waitFor(t, m, "cl1", "worker", StateCheckedIn)
}

func TestPreflightFailureIsolatesNode(t *testing.T) {
	exec := NewFakeExecutor()
	exec.FailPreflight["bad"] = true
	m := newTestManager(t, exec)
	m.Start("cl1", []Node{
		{Hostname: "bad", MachineID: "m1"},
		{Hostname: "good", MachineID: "m2"},
	}, "good")
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
	m.Start("cl9", []Node{{Hostname: "n1", MachineID: "m1"}}, "n1")
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
	m.Start("clc", []Node{{Hostname: "n1", MachineID: "m1"}}, "n1")
	waitFor(t, m, "clc", "n1", StateNetbooting)
	m.Cancel("clc")
	// State should stop advancing to imaged.
	time.Sleep(20 * time.Millisecond)
	d, _ := m.Status("clc")
	if d.Nodes["n1"].State == StateImaged || d.Nodes["n1"].State == StateDone {
		t.Fatalf("cancel did not stop stepping: %s", d.Nodes["n1"].State)
	}
}
