package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
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
	// RecordID is the BMC's SEL record handle for this entry. Used to anchor
	// freshness on the SEL's own log order rather than on the BMC clock.
	RecordID uint16
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

// GateWriter drops staged "go" records on a node's BMC over LAN, so an agent
// (which may have no in-band network) sees them via local KCS. The stage byte
// distinguishes restore / reboot / apply authorizations. WriteGate takes the
// node's whole authorized set, not one stage: the caller re-asserts every gate
// it has granted so far, so a SEL that lost records can be healed. ClearSEL
// wipes a node's SEL at deploy start so a prior run's records can't replay.
type GateWriter interface {
	WriteGate(ctx context.Context, n Node, stages ...byte) error
	ClearSEL(ctx context.Context, n Node) error
}

// bmcLocks serializes SEL work per BMC address. A BMC handles concurrent RMCP+
// sessions poorly, and two writers on one SEL can wipe each other's records.
var bmcLocks sync.Map // BMC address -> *sync.Mutex

// lockBMC takes the node's BMC lock and returns its release func.
func lockBMC(addr string) func() {
	v, _ := bmcLocks.LoadOrStore(addr, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// selFull reports whether an IPMI error is the BMC's "out of space" (0xC4)
// completion code — the only failure a SEL wipe can fix. Every other error
// (a lost UDP ack, a busy BMC) leaves the SEL intact, so wiping on those just
// destroys gate records that are already authorized.
func selFull(err error) bool {
	if err == nil {
		return false
	}
	var cc interface{ CompletionCode() goipmi.CompletionCode }
	if errors.As(err, &cc) {
		return cc.CompletionCode() == goipmi.CompletionCodeOutOfSpace
	}
	return false
}

// gatesPresent reads the node's SEL and returns the gate stages it holds.
func gatesPresent(ctx context.Context, client *goipmi.Client) (map[byte]bool, error) {
	entries, err := client.GetSELEntries(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := map[byte]bool{}
	for _, e := range entries {
		if e == nil || e.OEMTimestamped == nil ||
			e.OEMTimestamped.ManufacturerID != cubeManufacturerID {
			continue
		}
		o := e.OEMTimestamped.OEMDefined
		if o[0] == 0x30 && o[1] == 0x05 {
			out[o[2]] = true
		}
	}
	return out, nil
}

// IPMIGateWriter adds the cube "gate/go" OEM SEL records over IPMI LAN.
type IPMIGateWriter struct{}

// WriteGate makes the SEL hold exactly the authorized stages — one session,
// one BMC at a time. Read-after-write: the AddSELEntry ack can be lost to a UDP
// timeout even when the record landed (and can fail without landing), so the
// write's return can't be trusted — only the SEL contents can. Already-present
// stages are left alone, so re-asserting is cheap and idempotent.
func (IPMIGateWriter) WriteGate(ctx context.Context, n Node, stages ...byte) error {
	if len(stages) == 0 {
		return nil
	}
	defer lockBMC(n.BMCAddress)()

	client, err := dial(ctx, n)
	if err != nil {
		return err
	}
	defer client.Close(ctx)

	const attempts = 4
	var lastErr error
	for i := 0; i <= attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(i) * time.Second):
			}
		}
		present, gerr := gatesPresent(ctx, client)
		if gerr != nil {
			lastErr = gerr
			continue
		}
		var missing []byte
		for _, s := range stages {
			if !present[s] {
				missing = append(missing, s)
			}
		}
		if len(missing) == 0 {
			return nil
		}
		lastErr = fmt.Errorf("gate stages %v not present in SEL after write", missing)
		if i == attempts {
			break // the last pass is verify-only
		}
		// A stage missing while others are present means the SEL lost a record
		// the node still needs — the failure this recovery exists for. Say so:
		// silent self-healing hides how often it is happening.
		if len(missing) < len(stages) {
			log.Printf("gate heal %s: stage(s) %v missing from SEL — re-adding", n.Hostname, missing)
		}
		for _, s := range missing {
			if _, werr := client.AddSELEntry(ctx, gateSEL(s)); werr != nil {
				lastErr = werr
				// Only "out of space" is fixable by a wipe. The wipe also drops
				// the stages already authorized, so the next pass re-adds every
				// missing one — never just this stage.
				if selFull(werr) {
					if res, rerr := client.ReserveSEL(ctx); rerr == nil {
						_, _ = client.ClearSEL(ctx, res.ReservationID)
					}
				}
			}
		}
	}
	return lastErr
}

// ClearSEL wipes the node's SEL (reserve + clear) so a previous deploy's gate/
// status records don't satisfy this run's gates or replay old status.
func (IPMIGateWriter) ClearSEL(ctx context.Context, n Node) error {
	defer lockBMC(n.BMCAddress)()
	client, err := dial(ctx, n)
	if err != nil {
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
	// Observe returns the latest actionable Cube OEM status record that appears
	// AFTER the anchor entry (RecordID afterID) in the SEL's log order, or nil if
	// none. afterID==0 considers the whole log. Records at/before the anchor are
	// this-or-prior-deploy leftovers and are skipped — the freshness floor is the
	// SEL's own record handles, never the BMC clock.
	Observe(ctx context.Context, n Node, afterID uint16) (*SELStatus, error)
	// Anchor returns the RecordID of the last entry currently in the node's SEL
	// (0 if empty), captured at deploy start to floor Observe.
	Anchor(ctx context.Context, n Node) (uint16, error)
}

// IPMISELObserver reads SEL entries from a node's BMC over IPMI LAN and returns
// the most recent Cube OEM record.
type IPMISELObserver struct{}

func (IPMISELObserver) Observe(ctx context.Context, n Node, afterID uint16) (*SELStatus, error) {
	client, err := dial(ctx, n)
	if err != nil {
		return nil, err
	}
	defer client.Close(ctx)
	entries, err := client.GetSELEntries(ctx, 0)
	if err != nil {
		return nil, err
	}
	// GetSELEntries returns records in log (insertion) order. Walk it, and only
	// start considering records once we're past the anchor entry — its RecordID
	// is a handle, so we locate it by identity, not by numeric comparison (IPMI
	// does not guarantee record IDs are ordered). A missing anchor (cleared/
	// wrapped since capture) means the log is all newer than the deploy: consider
	// everything. The chosen record is the LAST one that maps to a real state, so
	// a trailing gate record (which carries no phase) can't mask a status.
	past := afterID == 0
	var latest *SELStatus
	for _, e := range entries {
		if !past {
			if e != nil && e.RecordID == afterID {
				past = true
			}
			continue
		}
		s := decodeCubeSEL(e)
		if s == nil || selState(*s) == "" {
			continue
		}
		latest = s
	}
	return latest, nil
}

// Anchor returns the RecordID of the most recent SEL entry on the node's BMC, or
// 0 if the log is empty. Called right after the deploy-start ClearSEL so the
// observer can ignore everything up to this point without trusting the clock.
func (IPMISELObserver) Anchor(ctx context.Context, n Node) (uint16, error) {
	client, err := dial(ctx, n)
	if err != nil {
		return 0, err
	}
	defer client.Close(ctx)
	entries, err := client.GetSELEntries(ctx, 0)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 || entries[len(entries)-1] == nil {
		return 0, nil
	}
	return entries[len(entries)-1].RecordID, nil
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
		Phase:    selPhaseName[oem[0]],
		Result:   selResultName[oem[1]],
		Detail:   strings.TrimRight(string(oem[2:]), "\x00"),
		At:       e.OEMTimestamped.Timestamp,
		RecordID: e.RecordID,
	}
}

// selState maps an OOB SEL phase/result to the orchestrator state it confirms.
func selState(s SELStatus) State {
	// An error result is terminal whatever the phase byte — a mis-encoded phase
	// (e.g. an agent writing "apply" → 0x00) must not make a real failure
	// invisible OOB, which is exactly the path we fall back to when in-band is
	// gone. Only the apply-failure record carries the error result.
	if s.Result == "error" {
		return StateError
	}
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
	defer lockBMC(n.BMCAddress)()
	client, err := dial(ctx, n)
	if err != nil {
		return err
	}
	defer client.Close(ctx)
	rec := endpointSEL(ip, port)
	if _, err := client.AddSELEntry(ctx, rec); err != nil {
		// Only "out of space" is fixable by a wipe, and wiping costs the node
		// every gate authorized so far — the gate reconciler puts those back.
		if !selFull(err) {
			return err
		}
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
