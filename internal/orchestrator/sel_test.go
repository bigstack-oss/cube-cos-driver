package orchestrator

import (
	"context"
	"sync"
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

// fakeSELObserver models a BMC SEL as an ordered record log with record-ID
// handles, so Observe/Anchor behave like the real (clock-independent) observer:
// Anchor returns the last record's ID, Observe returns the latest actionable
// record that follows the anchor in log order.
type fakeSELObserver struct {
	mu   sync.Mutex
	recs []SELStatus
}

func (f *fakeSELObserver) add(s SELStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s.RecordID = uint16(len(f.recs) + 1)
	f.recs = append(f.recs, s)
}

func (f *fakeSELObserver) Anchor(context.Context, Node) (uint16, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.recs) == 0 {
		return 0, nil
	}
	return f.recs[len(f.recs)-1].RecordID, nil
}

func (f *fakeSELObserver) Observe(_ context.Context, _ Node, afterID uint16) (*SELStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	past := afterID == 0
	var latest *SELStatus
	for i := range f.recs {
		if !past {
			if f.recs[i].RecordID == afterID {
				past = true
			}
			continue
		}
		if selState(f.recs[i]) == "" {
			continue
		}
		s := f.recs[i]
		latest = &s
	}
	return latest, nil
}

func TestPollSELIgnoresRecordsFromAPreviousDeploy(t *testing.T) {
	m := newTestManager(t, NewFakeExecutor())
	// A terminal record left over from a prior run. Its timestamp is deliberately
	// in the FUTURE: the freshness floor is the SEL anchor (record handle), not
	// the clock, so even a future-stamped stale record must not leak through.
	obs := &fakeSELObserver{}
	obs.add(SELStatus{Phase: "done", Result: "ok", At: time.Now().Add(time.Hour)})
	m.SetSELObserver(obs)
	m.Start("cl1", []Node{{Hostname: "cube-1", MachineID: "m1"}}, "cube-1", nil, false, "", false, false)
	waitFor(t, m, "cl1", "cube-1", StateImaged)

	// Give pollSEL several ticks: the stale record (at/before the anchor) must
	// never complete the node.
	time.Sleep(50 * time.Millisecond)
	if got := m.state("cl1", "cube-1"); got == StateDone {
		t.Fatal("stale SEL record from a previous deploy completed a fresh deploy")
	}

	// A fresh record appended after the anchor must merge via the poll loop.
	obs.add(SELStatus{Phase: "applied", Result: "ok", At: time.Now()})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && m.state("cl1", "cube-1") != StateDone {
		time.Sleep(2 * time.Millisecond)
	}
	if got := m.state("cl1", "cube-1"); got != StateDone {
		t.Fatalf("fresh SEL record did not merge, state = %s", got)
	}
	// Let the all-done verify goroutine finish so its store write can't race
	// t.TempDir cleanup.
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if d, _ := m.Status("cl1"); d.ClusterReady {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRestoreDoneAdvancesFromPreflightOK covers the node that reached (and
// finished) restore via its BMC SEL gate without a second greenlight HTTP
// round-trip: the driver's state is still PreflightOK, and the in-band
// restore-done report must advance it to rebooting rather than be dropped.
func TestRestoreDoneAdvancesFromPreflightOK(t *testing.T) {
	m := newTestManager(t, NewFakeExecutor())
	defer m.StopAll()
	m.Start("cl1", []Node{{Hostname: "cube-1", MachineID: "m1"}}, "cube-1", nil, true, "", false, false)
	// Park the node where GreenLight1 leaves it when the gate rode the BMC: the
	// greenlight HTTP set "waiting for the restore gate" and never came back.
	m.set("cl1", "cube-1", func(nd *NodeDeploy) {
		nd.State = StatePreflightOK
		nd.Message = "preflight passed — waiting for the restore gate on the BMC"
	})

	m.RestoreDone("cl1", "cube-1")

	if got := m.state("cl1", "cube-1"); got != StateRebooting {
		t.Fatalf("restore-done from PreflightOK did not advance: state = %s", got)
	}
}

// TestRestoreDoneNeverRegresses guards the advance window: a node the agent has
// already pushed past reboot must not be dragged back by a late restore-done.
func TestRestoreDoneNeverRegresses(t *testing.T) {
	m := newTestManager(t, NewFakeExecutor())
	defer m.StopAll()
	m.Start("cl1", []Node{{Hostname: "cube-1", MachineID: "m1"}}, "cube-1", nil, true, "", false, false)
	m.set("cl1", "cube-1", func(nd *NodeDeploy) { nd.State = StateApplying })

	m.RestoreDone("cl1", "cube-1")

	if got := m.state("cl1", "cube-1"); got != StateApplying {
		t.Fatalf("restore-done regressed an applying node to %s", got)
	}
}
