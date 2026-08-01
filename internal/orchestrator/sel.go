package orchestrator

import (
	"context"
	"fmt"
	"net"
	"strconv"
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
	0x10: "preflight", 0x16: "restore-done", 0x18: "rebooted",
	0x20: "applying", 0x21: "applied", 0x2f: "done", 0xff: "error",
}

// Gate stages (OEM byte 2 of a gate/go record) — must match the agent.
const (
	gateStageRestore  byte = 1
	gateStageReboot   byte = 2
	gateStageApply    byte = 3
	gateStageSetReady byte = 4
)

var selResultName = map[byte]string{
	0x01: "ok", 0x02: "degraded", 0x03: "unreachable", 0x04: "topology-error", 0xff: "error",
}

// GateWriter drops a staged "go" record on a node's BMC over LAN, so an agent
// (which may have no in-band network) sees it via local KCS. The stage byte
// distinguishes restore / reboot / apply authorizations. ClearSEL wipes a
// node's SEL at deploy start so a prior run's records can't replay.
type GateWriter interface {
	WriteGate(ctx context.Context, n Node, stage byte) error
	ClearSEL(ctx context.Context, n Node) error
}

// IPMIGateWriter adds the cube "gate/go" OEM SEL record over IPMI LAN.
type IPMIGateWriter struct{}

func (IPMIGateWriter) WriteGate(ctx context.Context, n Node, stage byte) error {
	client, err := goipmi.NewClient(n.BMCAddress, 623, n.BMCUser, n.BMCPass)
	if err != nil {
		return err
	}
	if err := client.Connect(ctx); err != nil {
		return err
	}
	defer client.Close(ctx)

	if _, err := client.AddSELEntry(ctx, gateSEL(stage)); err != nil {
		// SEL full ("out of space", cc 0xc4) — clear it and retry once. The SEL
		// here only carries our coordination records, so clearing is safe.
		if res, rerr := client.ReserveSEL(ctx); rerr == nil {
			if _, cerr := client.ClearSEL(ctx, res.ReservationID); cerr == nil {
				time.Sleep(2 * time.Second)
				_, err = client.AddSELEntry(ctx, gateSEL(stage))
			}
		}
		return err
	}
	return nil
}

// ClearSEL wipes the node's SEL (reserve + clear) so a previous deploy's gate/
// status records don't satisfy this run's gates or replay old status.
func (IPMIGateWriter) ClearSEL(ctx context.Context, n Node) error {
	client, err := goipmi.NewClient(n.BMCAddress, 623, n.BMCUser, n.BMCPass)
	if err != nil {
		return err
	}
	if err := client.Connect(ctx); err != nil {
		return err
	}
	defer client.Close(ctx)
	res, err := client.ReserveSEL(ctx)
	if err != nil {
		return err
	}
	_, err = client.ClearSEL(ctx, res.ReservationID)
	return err
}

// gateSEL builds the cube "gate/go" OEM SEL record for a stage (byte 2).
func gateSEL(stage byte) *goipmi.SEL {
	var oem [6]byte
	oem[0] = 0x30  // gate phase
	oem[1] = 0x05  // go result
	oem[2] = stage // restore / reboot / apply
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
	case "restore-done":
		return StateRebooting // installer finished restore; node is rebooting
	case "rebooted":
		return StateWaiting // OS-phase agent is up, holding for the apply gate
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
		m.maybeAutoSetReady(clusterID) // auto mode: trigger set_ready once all done
	}
}

// cubeEndpointManufacturerID marks the OEM SEL record that carries the
// driver's node-reachable endpoint (IPv4+port). Distinct from
// cubeManufacturerID (gate/status) so all 6 OEM bytes are free for the
// address — the record type is discriminated by this ID.
const cubeEndpointManufacturerID uint32 = 0x0BC0DF

// encodeEndpoint packs a node-reachable IPv4:port into the 6 OEM bytes:
// bytes 0-3 = IPv4, bytes 4-5 = port (big-endian).
func encodeEndpoint(ip [4]byte, port uint16) [6]byte {
	var b [6]byte
	copy(b[0:4], ip[:])
	b[4] = byte(port >> 8)
	b[5] = byte(port)
	return b
}

// decodeEndpoint reverses encodeEndpoint. ok=false if the address is unset
// (all-zero IPv4), so a stray/empty record never overrides the cmdline.
func decodeEndpoint(b [6]byte) (ip [4]byte, port uint16, ok bool) {
	copy(ip[:], b[0:4])
	port = uint16(b[4])<<8 | uint16(b[5])
	if ip == [4]byte{} {
		return ip, port, false
	}
	return ip, port, true
}

// endpointSEL builds the driver-endpoint OEM SEL record (advertise address).
func endpointSEL(ip [4]byte, port uint16) *goipmi.SEL {
	return &goipmi.SEL{
		RecordType: goipmi.SELRecordType(0xC0),
		OEMTimestamped: &goipmi.SELOEMTimestamped{
			Timestamp:      time.Now(),
			ManufacturerID: cubeEndpointManufacturerID,
			OEMDefined:     encodeEndpoint(ip, port),
		},
	}
}

// WriteEndpoint drops the driver-endpoint record on a node's BMC over LAN so
// the agent (which may have no in-band route to a shared PXE server's default
// driver) learns which driver actually booted it. Best-effort, same SEL-full
// recovery as WriteGate.
func (IPMIGateWriter) WriteEndpoint(ctx context.Context, n Node, ip [4]byte, port uint16) error {
	client, err := goipmi.NewClient(n.BMCAddress, 623, n.BMCUser, n.BMCPass)
	if err != nil {
		return err
	}
	if err := client.Connect(ctx); err != nil {
		return err
	}
	defer client.Close(ctx)
	rec := endpointSEL(ip, port)
	if _, err := client.AddSELEntry(ctx, rec); err != nil {
		if res, rerr := client.ReserveSEL(ctx); rerr == nil {
			if _, cerr := client.ClearSEL(ctx, res.ReservationID); cerr == nil {
				time.Sleep(2 * time.Second)
				_, err = client.AddSELEntry(ctx, rec)
			}
		}
		return err
	}
	return nil
}

// ParseAdvertise parses an "ip:port" advertise address into the SEL-record
// form (IPv4 bytes + uint16 port). Rejects non-IPv4 and out-of-range ports.
func ParseAdvertise(s string) (ip [4]byte, port uint16, err error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return ip, 0, err
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p < 1 || p > 65535 {
		return ip, 0, fmt.Errorf("invalid port %q", portStr)
	}
	v4 := net.ParseIP(host).To4()
	if v4 == nil {
		return ip, 0, fmt.Errorf("not an IPv4 address: %q", host)
	}
	copy(ip[:], v4)
	return ip, uint16(p), nil
}
