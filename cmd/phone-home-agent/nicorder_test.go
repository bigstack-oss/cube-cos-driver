package main

import (
	"sort"
	"testing"
)

// qa37-r630 reported its ports in driver-probe order (two Mellanox, two Intel
// interleaved); CubeCOS enumerates them in PCI bus order.
func TestNICPCIOrder(t *testing.T) {
	in := []nicInv{
		{Name: "eth0", PCIAddr: "0000:01:00.0"},
		{Name: "eth1", PCIAddr: "0000:04:00.0"},
		{Name: "eth2", PCIAddr: "0000:01:00.1"},
		{Name: "eth3", PCIAddr: "0000:04:00.1"},
	}
	sort.SliceStable(in, func(i, j int) bool { return pciLess(in[i].PCIAddr, in[j].PCIAddr) })
	want := []string{"0000:01:00.0", "0000:01:00.1", "0000:04:00.0", "0000:04:00.1"}
	for i, w := range want {
		if in[i].PCIAddr != w {
			t.Fatalf("position %d: got %s, want %s", i, in[i].PCIAddr, w)
		}
	}
}

func TestPCILessHexAndUnparseable(t *testing.T) {
	if !pciLess("0000:09:00.0", "0000:1a:00.0") {
		t.Error("hex bus ids must sort numerically, not lexically")
	}
	if !pciLess("0000:00:1f.6", "0001:00:00.0") {
		t.Error("domain must outrank bus")
	}
	if !pciLess("0000:01:00.0", "") {
		t.Error("an absent address must sort last")
	}
	if pciLess("", "0000:01:00.0") {
		t.Error("an absent address must not sort first")
	}
}
