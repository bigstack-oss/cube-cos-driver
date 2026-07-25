package orchestrator

import (
	"context"
	"strings"
	"time"

	goipmi "github.com/bougou/go-ipmi"
)

// cubeManufacturerID marks OEM SEL records written by the phone-home agent
// (must match cmd/phone-home-agent). The server's observer filters on it.
const cubeManufacturerID uint32 = 0x0BC0DE

// SELStatus is one decoded Cube OEM status record read from a node's BMC.
type SELStatus struct {
	Phase  string
	Result string
	Detail string
	At     time.Time
}

// reverse maps of the agent's compact encoding.
var selPhaseName = map[byte]string{
	0x10: "preflight", 0x20: "applying", 0x21: "applied", 0x2f: "done", 0xff: "error",
}
var selResultName = map[byte]string{
	0x01: "ok", 0x02: "degraded", 0x03: "unreachable", 0x04: "topology-error", 0xff: "error",
}

// GateWriter drops the master-done "go" record on a node's BMC over LAN, so a
// non-master agent (which has no in-band network yet) sees it via local KCS.
type GateWriter interface {
	WriteGate(ctx context.Context, n Node) error
}

// IPMIGateWriter adds the cube "gate/go" OEM SEL record over IPMI LAN.
type IPMIGateWriter struct{}

func (IPMIGateWriter) WriteGate(ctx context.Context, n Node) error {
	client, err := goipmi.NewClient(n.BMCAddress, 623, n.BMCUser, n.BMCPass)
	if err != nil {
		return err
	}
	if err := client.Connect(ctx); err != nil {
		return err
	}
	defer client.Close(ctx)

	if _, err := client.AddSELEntry(ctx, gateSEL()); err != nil {
		// SEL full ("out of space", cc 0xc4) — clear it and retry once. The SEL
		// here only carries our coordination records, so clearing is safe.
		if res, rerr := client.ReserveSEL(ctx); rerr == nil {
			if _, cerr := client.ClearSEL(ctx, res.ReservationID); cerr == nil {
				time.Sleep(2 * time.Second)
				_, err = client.AddSELEntry(ctx, gateSEL())
			}
		}
		return err
	}
	return nil
}

// gateSEL builds the cube "gate/go" OEM SEL record (master-done handoff).
func gateSEL() *goipmi.SEL {
	var oem [6]byte
	oem[0] = 0x30 // gate phase
	oem[1] = 0x05 // go result
	return &goipmi.SEL{
		RecordType: goipmi.SELRecordType(0xC0),
		OEMTimestamped: &goipmi.SELOEMTimestamped{
			Timestamp:      time.Now(),
			ManufacturerID: cubeManufacturerID,
			OEMDefined:     oem,
		},
	}
}

// SELObserver reads a node's out-of-band status from its BMC over the LAN.
type SELObserver interface {
	// Observe returns the latest Cube OEM SEL record for a node, or nil if none.
	Observe(ctx context.Context, n Node) (*SELStatus, error)
}

// IPMISELObserver reads SEL entries from a node's BMC over IPMI LAN and returns
// the most recent Cube OEM record.
type IPMISELObserver struct{}

func (IPMISELObserver) Observe(ctx context.Context, n Node) (*SELStatus, error) {
	client, err := goipmi.NewClient(n.BMCAddress, 623, n.BMCUser, n.BMCPass)
	if err != nil {
		return nil, err
	}
	if err := client.Connect(ctx); err != nil {
		return nil, err
	}
	defer client.Close(ctx)
	entries, err := client.GetSELEntries(ctx, 0)
	if err != nil {
		return nil, err
	}
	var latest *SELStatus
	for _, e := range entries {
		s := decodeCubeSEL(e)
		if s == nil {
			continue
		}
		if latest == nil || s.At.After(latest.At) {
			latest = s
		}
	}
	return latest, nil
}

// decodeCubeSEL returns the Cube status carried by a SEL entry, or nil if the
// entry isn't one of ours.
func decodeCubeSEL(e *goipmi.SEL) *SELStatus {
	if e == nil || e.OEMTimestamped == nil {
		return nil
	}
	if e.OEMTimestamped.ManufacturerID != cubeManufacturerID {
		return nil
	}
	oem := e.OEMTimestamped.OEMDefined
	return &SELStatus{
		Phase:  selPhaseName[oem[0]],
		Result: selResultName[oem[1]],
		Detail: strings.TrimRight(string(oem[2:]), "\x00"),
		At:     e.OEMTimestamped.Timestamp,
	}
}

// selState maps an OOB SEL phase/result to the orchestrator state it confirms.
func selState(s SELStatus) State {
	switch s.Phase {
	case "applying":
		return StateApplying
	case "applied", "done":
		return StateDone
	case "error":
		return StateError
	}
	return "" // preflight phase is confirmed in-band; nothing to advance OOB
}

// stateRank orders states along the deploy progression so a merge only ever
// advances a node (never regresses it).
func stateRank(s State) int {
	order := []State{
		StatePending, StateBMCPreflight, StateSetBootPXE, StatePowerCycle, StateNetbooting,
		StatePreflighting, StatePreflightOK, StateRestoring, StateRebooting,
		StateImaging, StateImaged, StateCheckedIn, StateWaiting, StateNetPreflight,
		StateApplying, StateApplied, StateDone,
	}
	for i, o := range order {
		if o == s {
			return i
		}
	}
	return -1
}

// MergeSEL folds an out-of-band SEL status into a node's deploy state. It only
// advances (never regresses) and is the fallback path when the in-band report
// is lost (e.g. mgmt moved off the flat L2 after apply). An OOB error is
// recorded only if the node isn't already terminal.
func (m *Manager) MergeSEL(clusterID, hostname string, s SELStatus) {
	target := selState(s)
	if target == "" {
		return
	}
	m.set(clusterID, hostname, func(nd *NodeDeploy) {
		if nd.State == StateDone {
			return
		}
		if target == StateError {
			if nd.State != StateError {
				nd.State = StateError
				nd.ErrCode = ErrApplyFailed
				nd.Message = "out-of-band: " + s.Detail
			}
			return
		}
		if stateRank(target) > stateRank(nd.State) {
			nd.State = target
		}
	})
	if target == StateDone {
		m.maybeVerifyCluster(clusterID)
	}
}
