package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/bigstack-oss/cube-cos-snapshot/internal/inventory"
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
