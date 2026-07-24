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

	"github.com/bigstack-oss/cube-cos-snapshot/internal/model"
)

// cubeManufacturerID marks our OEM SEL records so the server's observer can
// pick them out (arbitrary private-enterprise-style tag).
const cubeManufacturerID uint32 = 0x0BC0DE

// phaseCode maps a phase/result to a compact byte pair for the SEL OEM field.
var phaseCode = map[string]byte{
	"preflight": 0x10, "applying": 0x20, "applied": 0x21, "done": 0x2f, "error": 0xff,
}
var resultCode = map[string]byte{
	"ok": 0x01, "degraded": 0x02, "unreachable": 0x03, "topology-error": 0x04, "error": 0xff,
}

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

// labelDevMap maps a bundle's init IF labels (IF.1..) to kernel device names by
// enumeration order.
func labelDevMap(b model.PreflightBundle) map[string]string {
	var initLabels []string
	for _, l := range b.Links {
		if l.Type == "init" {
			initLabels = append(initLabels, l.Name)
		}
	}
	sort.Slice(initLabels, func(i, j int) bool { return ifIndex(initLabels[i]) < ifIndex(initLabels[j]) })
	phys := physIFs()
	m := map[string]string{}
	for i, lbl := range initLabels {
		if i < len(phys) {
			m[lbl] = phys[i]
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
func configureTopology(b model.PreflightBundle) error {
	labelDev := labelDevMap(b)
	_ = exec.Command("modprobe", "bonding").Run()
	_ = exec.Command("modprobe", "8021q").Run()

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
		if err := ipCmd("link", "add", l.Name, "type", "bond"); err != nil {
			return err
		}
		if err := ipCmd("link", "set", l.Name, "up"); err != nil {
			return err
		}
		for _, mLabel := range l.Members {
			dev := labelDev[mLabel]
			if dev == "" {
				return fmt.Errorf("bond %s: no device for member %s", l.Name, mLabel)
			}
			_ = ipCmd("link", "set", dev, "down")
			if err := ipCmd("link", "set", dev, "master", l.Name); err != nil {
				return err
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
			parent = l.Parent // parent may itself be a bond name
		}
		if err := ipCmd("link", "add", "link", parent, "name", l.Name, "type", "vlan", "id", strconv.Itoa(l.VLANID)); err != nil {
			return err
		}
	}
	for _, l := range b.Links {
		dev := devFor(l)
		if dev == "" {
			continue
		}
		_ = ipCmd("link", "set", dev, "up")
		if l.IP != "" {
			if err := ipCmd("addr", "add", l.IP, "dev", dev); err != nil {
				return err
			}
		}
	}
	return nil
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
