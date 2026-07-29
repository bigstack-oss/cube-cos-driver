package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/bigstack-oss/cube-cos-driver/internal/inventory"
	"github.com/stmcginnis/gofish/schemas"
)

type fake struct {
	inv inventory.Inventory
	err error
}

func (f fake) Discover(context.Context, Target) (inventory.Inventory, error) {
	return f.inv, f.err
}

func TestCombinedPrefersPrimaryWhenRich(t *testing.T) {
	c := Combined{
		Primary:   fake{inv: inventory.Inventory{Source: "redfish", CPUCount: 2}},
		Secondary: fake{inv: inventory.Inventory{Source: "ipmi", Serial: "SN"}},
	}
	inv, err := c.Discover(context.Background(), Target{})
	if err != nil || inv.Source != "redfish" {
		t.Fatalf("expected redfish, got %+v err %v", inv, err)
	}
}

func TestCombinedFallsBackOnPrimaryError(t *testing.T) {
	c := Combined{
		Primary:   fake{err: errors.New("no redfish")},
		Secondary: fake{inv: inventory.Inventory{Source: "ipmi", Serial: "SN"}},
	}
	inv, err := c.Discover(context.Background(), Target{})
	if err != nil || inv.Source != "ipmi" {
		t.Fatalf("expected ipmi fallback, got %+v err %v", inv, err)
	}
}

func TestCombinedFallsBackOnEmptyPrimary(t *testing.T) {
	c := Combined{
		Primary:   fake{inv: inventory.Inventory{Source: "redfish"}}, // no core facts
		Secondary: fake{inv: inventory.Inventory{Source: "ipmi", Serial: "SN"}},
	}
	inv, err := c.Discover(context.Background(), Target{})
	if err != nil || inv.Source != "ipmi" {
		t.Fatalf("expected ipmi fallback on empty primary, got %+v err %v", inv, err)
	}
}

func TestCombinedReturnsErrorWhenBothFail(t *testing.T) {
	c := Combined{
		Primary:   fake{err: errors.New("no redfish")},
		Secondary: fake{err: errors.New("no ipmi")},
	}
	if _, err := c.Discover(context.Background(), Target{}); err == nil {
		t.Fatal("expected error when both fail")
	}
}

func TestVolumeDiskMapsRAIDVirtualDisk(t *testing.T) {
	cap := 800166076416 // ~745 GiB
	v := &schemas.Volume{}
	v.Name = "Virtual Disk 0"
	v.DisplayName = "CubeCOS"
	v.RAIDType = "RAID1"
	v.CapacityBytes = &cap
	d := volumeDisk(v)
	if d.Name != "CubeCOS" {
		t.Fatalf("name = %q, want display name", d.Name)
	}
	if d.Type != "RAID1" || d.SizeBytes != int64(cap) {
		t.Fatalf("type/size = %q/%d", d.Type, d.SizeBytes)
	}
	// Explicit eligibility beats the UI's /virtual/ exclusion heuristic.
	if d.OSEligible == nil || !*d.OSEligible {
		t.Fatal("RAID virtual disk must be explicitly OS-eligible")
	}
}
