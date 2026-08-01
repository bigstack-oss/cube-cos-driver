package main

import (
	"os"
	"path/filepath"
	"testing"
)

// fakePCIDev creates a sysfs PCI device dir with class/vendor/device files.
func fakePCIDev(t *testing.T, root, addr, class, vendor, device string) {
	t.Helper()
	d := filepath.Join(root, "bus/pci/devices", addr)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, v := range map[string]string{"class": class, "vendor": vendor, "device": device} {
		if err := os.WriteFile(filepath.Join(d, f), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// GPU, FC HBA, InfiniBand, and accelerator PCI classes are reported as cards;
// plain Ethernet NICs and storage controllers are not.
func TestCardsInvClasses(t *testing.T) {
	root := t.TempDir()
	fakePCIDev(t, root, "0000:3b:00.0", "0x030200", "0x10de", "0x20b0") // NVIDIA A100 (3D controller)
	fakePCIDev(t, root, "0000:04:00.0", "0x0c0400", "0x1077", "0x2532") // QLogic FC HBA
	fakePCIDev(t, root, "0000:5e:00.0", "0x020700", "0x15b3", "0x1017") // Mellanox InfiniBand
	fakePCIDev(t, root, "0000:af:00.0", "0x120000", "0x1da3", "0x1020") // Habana accelerator
	fakePCIDev(t, root, "0000:01:00.0", "0x020000", "0x8086", "0x1521") // Intel Ethernet — not a card
	fakePCIDev(t, root, "0000:02:00.0", "0x010400", "0x1000", "0x005d") // MegaRAID — not a card
	fakePCIDev(t, root, "0000:09:00.0", "0x030000", "0x102b", "0x0534") // Matrox BMC VGA

	cards := cardsInvFrom(root)
	got := map[string]string{}
	for _, c := range cards {
		got[c.Slot] = c.Type + "|" + c.Name
	}
	want := map[string]string{
		"0000:3b:00.0": "GPU|NVIDIA 0x20b0",
		"0000:04:00.0": "FC HBA|QLogic 0x2532",
		"0000:5e:00.0": "InfiniBand|Mellanox 0x1017",
		"0000:af:00.0": "Accelerator|Habana 0x1020",
		"0000:09:00.0": "GPU|Matrox 0x0534",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d cards want %d: %v", len(got), len(want), got)
	}
	for slot, w := range want {
		if got[slot] != w {
			t.Errorf("slot %s = %q, want %q", slot, got[slot], w)
		}
	}
}

// An RDMA-capable Ethernet NIC (RoCE — class 0x0200 but with an
// /sys/class/infiniband entry) is reported as an RDMA NIC; a true InfiniBand
// device already reported by class is not duplicated.
func TestCardsInvRDMA(t *testing.T) {
	root := t.TempDir()
	fakePCIDev(t, root, "0000:5e:00.0", "0x020000", "0x15b3", "0x101d") // ConnectX-6 Ethernet (RoCE)
	fakePCIDev(t, root, "0000:5f:00.0", "0x020700", "0x15b3", "0x1017") // true InfiniBand
	// real sysfs layout: /sys/class/infiniband/<ibdev>/device -> PCI device dir
	for ib, addr := range map[string]string{"mlx5_0": "0000:5e:00.0", "mlx5_1": "0000:5f:00.0"} {
		d := filepath.Join(root, "class/infiniband", ib)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "bus/pci/devices", addr), filepath.Join(d, "device")); err != nil {
			t.Fatal(err)
		}
	}

	cards := cardsInvFrom(root)
	types := map[string]string{}
	for _, c := range cards {
		types[c.Slot] = c.Type
	}
	if types["0000:5e:00.0"] != "RDMA NIC" {
		t.Errorf("RoCE NIC type = %q, want RDMA NIC (cards: %v)", types["0000:5e:00.0"], cards)
	}
	if types["0000:5f:00.0"] != "InfiniBand" {
		t.Errorf("IB device type = %q, want InfiniBand", types["0000:5f:00.0"])
	}
	if len(cards) != 2 {
		t.Fatalf("want 2 cards (no duplicates), got %v", cards)
	}
}
