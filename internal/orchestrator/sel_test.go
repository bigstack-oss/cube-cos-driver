package orchestrator

import (
	"context"
	"testing"
	"time"

	goipmi "github.com/bougou/go-ipmi"
)

// mkSEL builds a timestamped OEM SEL record for decode tests.
func mkSEL(mfg uint32, phase, result byte, detail string) *goipmi.SEL {
	var oem [6]byte
	oem[0] = phase
	oem[1] = result
	copy(oem[2:], []byte(detail))
	return &goipmi.SEL{
		RecordType: goipmi.SELRecordType(0xC0),
		OEMTimestamped: &goipmi.SELOEMTimestamped{
			Timestamp:      time.Unix(1000, 0),
			ManufacturerID: mfg,
			OEMDefined:     oem,
		},
	}
}

func TestMergeSELAdvancesButNeverRegresses(t *testing.T) {
	m := newTestManager(t, NewFakeExecutor())
	fv := &fakeVerifier{}
	m.SetVerifier(fv)
	m.Start("cl1", []Node{{Hostname: "cube-1", MachineID: "m1"}}, "cube-1", nil, false, "", false, false)
	waitFor(t, m, "cl1", "cube-1", StateImaged)

	// OOB "applying" advances the node from imaged.
	m.MergeSEL("cl1", "cube-1", SELStatus{Phase: "applying", Result: "ok", At: time.Now()})
	if got := m.state("cl1", "cube-1"); got != StateApplying {
		t.Fatalf("state after applying SEL = %s", got)
	}

	// A stale "preflight" SEL must not regress it.
	m.MergeSEL("cl1", "cube-1", SELStatus{Phase: "preflight", Result: "ok"})
	if got := m.state("cl1", "cube-1"); got != StateApplying {
		t.Fatalf("preflight SEL regressed state to %s", got)
	}

	// OOB "applied" confirms done even without an in-band report.
	m.MergeSEL("cl1", "cube-1", SELStatus{Phase: "applied", Result: "ok"})
	if got := m.state("cl1", "cube-1"); got != StateDone {
		t.Fatalf("state after applied SEL = %s", got)
	}
	// Wait for the all-done verify goroutine to finish so its store write can't
	// race t.TempDir cleanup.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if d, _ := m.Status("cl1"); d.ClusterReady {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// A later "applying" cannot regress a done node.
	m.MergeSEL("cl1", "cube-1", SELStatus{Phase: "applying", Result: "ok"})
	if got := m.state("cl1", "cube-1"); got != StateDone {
		t.Fatalf("done node regressed to %s", got)
	}
}

func TestMergeSELError(t *testing.T) {
	m := newTestManager(t, NewFakeExecutor())
	m.Start("cl1", []Node{{Hostname: "cube-1", MachineID: "m1"}}, "cube-1", nil, false, "", false, false)
	waitFor(t, m, "cl1", "cube-1", StateImaged)
	m.MergeSEL("cl1", "cube-1", SELStatus{Phase: "error", Result: "error", Detail: "apply blew up"})
	d, _ := m.Status("cl1")
	if d.Nodes["cube-1"].State != StateError || d.Nodes["cube-1"].ErrCode != ErrApplyFailed {
		t.Fatalf("error SEL not recorded: %+v", d.Nodes["cube-1"])
	}
}

func TestDecodeCubeSELFiltersForeignRecords(t *testing.T) {
	// A non-Cube manufacturer ID is ignored.
	if decodeCubeSEL(mkSEL(0x123456, 0x20, 0x01, "")) != nil {
		t.Fatal("foreign SEL record should be ignored")
	}
	got := decodeCubeSEL(mkSEL(cubeManufacturerID, 0x21, 0x01, "d"))
	if got == nil || got.Phase != "applied" || got.Result != "ok" || got.Detail != "d" {
		t.Fatalf("decode = %+v", got)
	}
}

// fakeSELObserver returns a canned status for every node.
type fakeSELObserver struct{ s *SELStatus }

func (f fakeSELObserver) Observe(context.Context, Node) (*SELStatus, error) { return f.s, nil }

func TestPollSELIgnoresRecordsFromAPreviousDeploy(t *testing.T) {
	m := newTestManager(t, NewFakeExecutor())
	// A terminal record stamped an hour ago — i.e. left over from a prior run.
	stale := &SELStatus{Phase: "done", Result: "ok", At: time.Now().Add(-time.Hour)}
	m.SetSELObserver(fakeSELObserver{s: stale})
	m.Start("cl1", []Node{{Hostname: "cube-1", MachineID: "m1"}}, "cube-1", nil, false, "", false, false)
	waitFor(t, m, "cl1", "cube-1", StateImaged)

	// Give pollSEL several ticks: the stale record must never complete the node.
	time.Sleep(50 * time.Millisecond)
	if got := m.state("cl1", "cube-1"); got == StateDone {
		t.Fatal("stale SEL record from a previous deploy completed a fresh deploy")
	}

	// A fresh record (stamped now) must still merge.
	m.MergeSEL("cl1", "cube-1", SELStatus{Phase: "applied", Result: "ok", At: time.Now()})
	if got := m.state("cl1", "cube-1"); got != StateDone {
		t.Fatalf("fresh SEL record did not merge, state = %s", got)
	}
	// Let the all-done verify goroutine finish so its store write can't race
	// t.TempDir cleanup.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if d, _ := m.Status("cl1"); d.ClusterReady {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
}
