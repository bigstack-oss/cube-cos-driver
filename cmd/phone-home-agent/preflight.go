// Installer-phase (pre-restore) side effects: stand up the appointed network
// topology transiently, check bond-member carrier, and mirror status to the
// BMC via SEL over the local KCS interface. Exercised on the lab; unit tests
// cover the flow logic in internal/agent.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	goipmi "github.com/bougou/go-ipmi"

	"github.com/bigstack-oss/cube-cos-driver/internal/agent"
	"github.com/bigstack-oss/cube-cos-driver/internal/model"
)

// cubeManufacturerID marks our OEM SEL records so the server's observer can
// pick them out (arbitrary private-enterprise-style tag).
const cubeManufacturerID uint32 = 0x0BC0DE

// phaseCode maps a phase/result to a compact byte pair for the SEL OEM field.
var phaseCode = map[string]byte{
	"preflight": 0x10, "applying": 0x20, "applied": 0x21, "done": 0x2f, "gate": 0x30, "error": 0xff,
}
var resultCode = map[string]byte{
	"ok": 0x01, "degraded": 0x02, "unreachable": 0x03, "topology-error": 0x04, "go": 0x05, "error": 0xff,
}

// selGateGo is the OEM byte pair the server writes to a non-master node's BMC
// SEL (over LAN) once the master's apply is done — the OOB "your turn" signal.
var selGateGo = [2]byte{phaseCode["gate"], resultCode["go"]}

// physIFs returns physical NIC kernel names sorted by PCI address so the k-th
// entry corresponds to CubeCOS's IF.k enumeration.
func physIFs() []string {
	type dev struct{ name, pci string }
	var devs []dev
	entries, _ := os.ReadDir("/sys/class/net")
	for _, e := range entries {
		n := e.Name()
		if n == "lo" {
			continue
		}
		// Physical devices have a device symlink into the PCI tree.
		link, err := os.Readlink(filepath.Join("/sys/class/net", n, "device"))
		if err != nil {
			continue // virtual (bond/vlan/bridge) — skip
		}
		devs = append(devs, dev{name: n, pci: link})
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i].pci < devs[j].pci })
	out := make([]string, len(devs))
	for i, d := range devs {
		out[i] = d.name
	}
	return out
}

// labelDevMap maps a bundle's init IF labels (IF.N) to kernel device names.
// CubeCOS numbers interfaces by PCI enumeration order, so IF.N is the N-th
// physical NIC (1-indexed) — NOT the N-th *enabled* one. Mapping positionally
// would put e.g. IF.5 on the 2nd NIC when only IF.1+IF.5 are enabled.
func labelDevMap(b model.PreflightBundle) map[string]string {
	phys := physIFs()
	m := map[string]string{}
	for _, l := range b.Links {
		if l.Type != "init" {
			continue
		}
		idx := ifIndex(l.Name) - 1 // IF.1 → phys[0], IF.5 → phys[4]
		if idx >= 0 && idx < len(phys) {
			m[l.Name] = phys[idx]
		}
	}
	return m
}

// ifIndex parses the trailing number of an "IF.N" label (0 if absent).
func ifIndex(label string) int {
	i := strings.LastIndex(label, ".")
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(label[i+1:])
	return n
}

func ipCmd(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// configureTopology stands up the node's bonds, VLANs, and role IPs transiently
// (torn down by the restore + reboot). Kernel devices for a link:
// init → its physical NIC; bond/vlan → the link name itself.
// configureTopology stands up the node's transient topology best-effort: it
// continues past individual failures and returns a combined diagnostic string
// (nil if everything succeeded) so the server can see exactly what happened.
func configureTopology(b model.PreflightBundle) (string, error) {
	// Teardown-first: remove links a previous round created so an
	// operator-rekicked bundle applies cleanly (no "exists" failures).
	teardownTopology()
	labelDev := labelDevMap(b)
	_ = exec.Command("modprobe", "bonding").Run()
	_ = exec.Command("modprobe", "8021q").Run()

	var errs []string
	note := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	// mapping diagnostic: resolved IF→device + physical carrier/operstate, so
	// the server can see which NIC each role landed on.
	var mapDiag []string
	for _, l := range b.Links {
		if l.Type == "init" {
			dev := labelDev[l.Name]
			mapDiag = append(mapDiag, fmt.Sprintf("%s->%s[%s]", l.Name, dev, nicState(dev)))
		}
	}
	diag := "map: " + strings.Join(mapDiag, " ") + " | phys: " + strings.Join(allNICStates(), " ")

	devFor := func(l model.PfLink) string {
		if l.Type == "init" {
			return labelDev[l.Name]
		}
		return l.Name
	}

	// Bonds first (so VLANs can ride them), then VLANs, then addresses.
	for _, l := range b.Links {
		if l.Type != "bond" {
			continue
		}
		_ = ipCmd("link", "del", l.Name) // idempotent re-run
		if err := ipCmd("link", "add", l.Name, "type", "bond"); err != nil {
			note("bond %s add: %v", l.Name, err)
		}
		_ = ipCmd("link", "set", l.Name, "up")
		for _, mLabel := range l.Members {
			dev := labelDev[mLabel]
			if dev == "" {
				note("bond %s: no device for member %s", l.Name, mLabel)
				continue
			}
			_ = ipCmd("link", "set", dev, "down")
			if err := ipCmd("link", "set", dev, "master", l.Name); err != nil {
				note("enslave %s→%s: %v", dev, l.Name, err)
			}
			_ = ipCmd("link", "set", dev, "up")
		}
	}
	for _, l := range b.Links {
		if l.Type != "vlan" {
			continue
		}
		parent := labelDev[l.Parent]
		if parent == "" {
			parent = l.Parent
		}
		_ = ipCmd("link", "del", l.Name) // idempotent re-run
		if err := ipCmd("link", "add", "link", parent, "name", l.Name, "type", "vlan", "id", strconv.Itoa(l.VLANID)); err != nil {
			note("vlan %s: %v", l.Name, err)
		}
	}
	for _, l := range b.Links {
		dev := devFor(l)
		if dev == "" {
			note("link %s: no device resolved", l.Name)
			continue
		}
		_ = ipCmd("link", "set", dev, "up")
		if l.IP != "" {
			if err := ipCmd("addr", "replace", l.IP, "dev", dev); err != nil {
				note("addr %s dev %s(%s): %v", l.IP, l.Name, dev, err)
			}
		}
	}
	recordCreatedLinks(b)
	if len(errs) > 0 {
		return diag, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return diag, nil
}

// nicState reads a device's carrier + operstate + speed for diagnostics.
func nicState(dev string) string {
	if dev == "" {
		return "no-dev"
	}
	rd := func(f string) string {
		b, _ := os.ReadFile("/sys/class/net/" + dev + "/" + f)
		return strings.TrimSpace(string(b))
	}
	return fmt.Sprintf("car=%s,op=%s,spd=%s", rd("carrier"), rd("operstate"), rd("speed"))
}

// allNICStates lists every physical NIC (PCI-ordered) with driver + carrier, so
// we can see the full enumeration the agent sees vs what CubeCOS expects.
func allNICStates() []string {
	var out []string
	for i, dev := range physIFs() {
		drv := ""
		if l, err := os.Readlink("/sys/class/net/" + dev + "/device/driver"); err == nil {
			drv = filepath.Base(l)
		}
		car, _ := os.ReadFile("/sys/class/net/" + dev + "/carrier")
		out = append(out, fmt.Sprintf("#%d=%s(%s,car=%s)", i, dev, drv, strings.TrimSpace(string(car))))
	}
	return out
}

// carrier reports whether every physical NIC that a bond enslaves has link
// carrier; a member with carrier down means the bond would come up degraded.
func carrier(b model.PreflightBundle) (bool, string) {
	labelDev := labelDevMap(b)
	for _, l := range b.Links {
		if l.Type != "bond" {
			continue
		}
		for _, mLabel := range l.Members {
			dev := labelDev[mLabel]
			if dev == "" {
				return false, fmt.Sprintf("%s: no device", mLabel)
			}
			data, err := os.ReadFile(filepath.Join("/sys/class/net", dev, "carrier"))
			if err != nil || strings.TrimSpace(string(data)) != "1" {
				return false, fmt.Sprintf("%s (%s) carrier down", mLabel, dev)
			}
		}
	}
	return true, ""
}

// writeSEL logs a compact OEM record to the local BMC over KCS (/dev/ipmi0),
// so the orchestrator can read node status out-of-band even when the data-plane
// network is down. Best effort — a missing/again unwritable BMC is not fatal.
func writeSEL(phase, result, detail string) error {
	client, err := goipmi.NewOpenClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		return err
	}
	defer client.Close(ctx)

	var oem [6]byte
	oem[0] = phaseCode[phase]
	oem[1] = resultCode[result]
	copy(oem[2:], []byte(detail)) // truncated to fit
	sel := &goipmi.SEL{
		RecordType: goipmi.SELRecordType(0xC0), // timestamped OEM range
		OEMTimestamped: &goipmi.SELOEMTimestamped{
			Timestamp:      time.Now(),
			ManufacturerID: cubeManufacturerID,
			OEMDefined:     oem,
		},
	}
	_, err = client.AddSELEntry(ctx, sel)
	return err
}

// selGatePresent reports whether the server's "go" record (written to this
// node's BMC over LAN once the master's apply finished) is in the local SEL,
// read out-of-band over KCS — no in-band network required.
func selGatePresent() bool {
	client, err := goipmi.NewOpenClient()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		return false
	}
	defer client.Close(ctx)
	entries, err := client.GetSELEntries(ctx, 0)
	if err != nil {
		return false
	}
	for _, e := range entries {
		o := e.OEMTimestamped
		if o != nil && o.ManufacturerID == cubeManufacturerID &&
			o.OEMDefined[0] == selGateGo[0] && o.OEMDefined[1] == selGateGo[1] {
			return true
		}
	}
	return false
}

// waitSELGate blocks until the go record appears in the local SEL or ctx ends.
func waitSELGate(ctx context.Context, poll time.Duration) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		if selGatePresent() {
			return true
		}
		time.Sleep(poll)
	}
}

// teardownTopology removes the transient links a previous preflight round
// created (bonds, VLANs) and flushes addresses, so an operator-rekicked bundle
// applies cleanly. Best effort — reads the recorded link list.
const createdLinksFile = "/run/preflight-links"

func recordCreatedLinks(b model.PreflightBundle) {
	labelDev := labelDevMap(b)
	var lines []string
	for _, l := range b.Links {
		if l.Type == "bond" || l.Type == "vlan" {
			lines = append(lines, "link:"+l.Name)
		}
		if l.IP != "" {
			dev := l.Name
			if l.Type == "init" {
				dev = labelDev[l.Name]
			}
			if dev != "" {
				lines = append(lines, "addr:"+dev)
			}
		}
	}
	_ = os.WriteFile(createdLinksFile, []byte(strings.Join(lines, "\n")), 0o644)
}

func teardownTopology() {
	data, err := os.ReadFile(createdLinksFile)
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		switch {
		case strings.HasPrefix(line, "link:"):
			_ = ipCmd("link", "del", strings.TrimPrefix(line, "link:"))
		case strings.HasPrefix(line, "addr:"):
			// Flush addresses this preflight added (plus the bootstrap DHCP one
			// — the re-added role IP covers the driver route, same as the
			// normal post-topology state).
			_ = ipCmd("addr", "flush", "dev", strings.TrimPrefix(line, "addr:"))
		}
	}
	_ = os.Remove(createdLinksFile)
}

// carrierChecks returns one report row per link the topology depends on: every
// bond member (named — so the report shows exactly which port is down), each
// bond device's operstate, and each non-bonded role link's carrier.
func carrierChecks(b model.PreflightBundle) []agent.PreflightResult {
	labelDev := labelDevMap(b)
	var rows []agent.PreflightResult
	for _, l := range b.Links {
		switch l.Type {
		case "bond":
			for _, mLabel := range l.Members {
				dev := labelDev[mLabel]
				target := fmt.Sprintf("%s member %s (%s) link", l.Name, mLabel, dev)
				if dev == "" {
					rows = append(rows, agent.PreflightResult{Target: target, OK: false, Detail: "no device resolved for this IF label"})
					continue
				}
				car, _ := os.ReadFile("/sys/class/net/" + dev + "/carrier")
				up := strings.TrimSpace(string(car)) == "1"
				rows = append(rows, agent.PreflightResult{Target: target, OK: up, Detail: nicState(dev)})
			}
			op, _ := os.ReadFile("/sys/class/net/" + l.Name + "/operstate")
			bondUp := strings.TrimSpace(string(op)) == "up"
			rows = append(rows, agent.PreflightResult{Target: l.Name + " bond device", OK: bondUp, Detail: "operstate " + strings.TrimSpace(string(op))})
		case "init":
			if len(l.Roles) == 0 {
				continue
			}
			dev := labelDev[l.Name]
			target := fmt.Sprintf("%s (%s) link [%s]", l.Name, dev, strings.Join(l.Roles, ","))
			if dev == "" {
				rows = append(rows, agent.PreflightResult{Target: target, OK: false, Detail: "no device resolved for this IF label"})
				continue
			}
			car, _ := os.ReadFile("/sys/class/net/" + dev + "/carrier")
			up := strings.TrimSpace(string(car)) == "1"
			rows = append(rows, agent.PreflightResult{Target: target, OK: up, Detail: nicState(dev)})
		}
	}
	return rows
}
