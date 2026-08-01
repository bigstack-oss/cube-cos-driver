package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type cardInv struct {
	Slot string `json:"slot"` // PCI address, e.g. 0000:3b:00.0
	Name string `json:"name"` // vendor + PCI device id (no pci.ids in the installer)
	Type string `json:"type"` // GPU | Accelerator | FC HBA | InfiniBand | RDMA NIC
}

var pciVendorNames = map[string]string{
	"0x10de": "NVIDIA",
	"0x1002": "AMD",
	"0x8086": "Intel",
	"0x15b3": "Mellanox",
	"0x1077": "QLogic",
	"0x10df": "Emulex",
	"0x14e4": "Broadcom",
	"0x1a03": "ASPEED",
	"0x102b": "Matrox",
	"0x10ee": "Xilinx",
	"0x1da3": "Habana",
}

// cardClass maps a PCI class code to a card type, empty for classes we don't
// report (NICs and storage are inventoried elsewhere).
func cardClass(class string) string {
	c := strings.TrimPrefix(strings.ToLower(class), "0x")
	switch {
	case strings.HasPrefix(c, "03"): // display controllers: VGA/XGA/3D
		return "GPU"
	case strings.HasPrefix(c, "1200"): // processing accelerators
		return "Accelerator"
	case strings.HasPrefix(c, "0c04"): // Fibre Channel
		return "FC HBA"
	case strings.HasPrefix(c, "0c06"), strings.HasPrefix(c, "0207"): // InfiniBand
		return "InfiniBand"
	}
	return ""
}

func cardsInv() []cardInv { return cardsInvFrom("/sys") }

// cardsInvFrom reports the add-in cards an operator assigns workloads by —
// GPUs, accelerators, FC HBAs, InfiniBand — from the sysfs PCI tree, plus
// RDMA-capable NICs (RoCE: Ethernet class but with an infiniband class entry).
// sysRoot is the /sys mount, parameterized for tests.
func cardsInvFrom(sysRoot string) []cardInv {
	readID := func(addr, f string) string {
		return strings.TrimSpace(readFileStr(filepath.Join(sysRoot, "bus/pci/devices", addr, f)))
	}
	name := func(addr string) string {
		v, d := readID(addr, "vendor"), readID(addr, "device")
		if n, ok := pciVendorNames[strings.ToLower(v)]; ok {
			v = n
		}
		return strings.TrimSpace(v + " " + d)
	}

	byAddr := map[string]cardInv{}
	entries, _ := os.ReadDir(filepath.Join(sysRoot, "bus/pci/devices"))
	for _, e := range entries {
		addr := e.Name()
		t := cardClass(readID(addr, "class"))
		if t == "" {
			continue
		}
		byAddr[addr] = cardInv{Slot: addr, Name: name(addr), Type: t}
	}

	// RDMA-capable NICs: any PCI device backing an /sys/class/infiniband entry.
	// True IB devices are already reported by class; Ethernet-class ones (RoCE)
	// are added as RDMA NICs.
	ibs, _ := os.ReadDir(filepath.Join(sysRoot, "class/infiniband"))
	for _, ib := range ibs {
		dest, err := os.Readlink(filepath.Join(sysRoot, "class/infiniband", ib.Name(), "device"))
		if err != nil {
			continue
		}
		addr := filepath.Base(dest)
		if _, seen := byAddr[addr]; seen {
			continue
		}
		byAddr[addr] = cardInv{Slot: addr, Name: name(addr), Type: "RDMA NIC"}
	}

	out := make([]cardInv, 0, len(byAddr))
	for _, c := range byAddr {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slot < out[j].Slot })
	return out
}
