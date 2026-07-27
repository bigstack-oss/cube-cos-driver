// Package discovery reads hardware facts from a machine's BMC. Redfish is
// tried first (rich data); IPMI FRU is a fallback. Real BMCs are never
// contacted in tests — the Discoverer interface is the seam for fakes.
package discovery

import (
	"context"
	"errors"

	"github.com/bigstack-oss/cube-cos-driver/internal/inventory"
)

type Target struct {
	Address  string
	Username string
	Password string
}

type Discoverer interface {
	Discover(ctx context.Context, t Target) (inventory.Inventory, error)
}

// hasCoreFacts reports whether a result is rich enough to skip the fallback.
func hasCoreFacts(inv inventory.Inventory) bool {
	return inv.Serial != "" || inv.CPUCount > 0 || len(inv.NICs) > 0 || inv.MemoryBytes > 0
}

// Combined tries primary (Redfish); on error or empty result, falls back to
// secondary (IPMI). Source is tagged by whichever produced the result.
type Combined struct {
	Primary   Discoverer
	Secondary Discoverer
}

func (c Combined) Discover(ctx context.Context, t Target) (inventory.Inventory, error) {
	var firstErr error
	if c.Primary != nil {
		inv, err := c.Primary.Discover(ctx, t)
		if err == nil && hasCoreFacts(inv) {
			return inv, nil
		}
		if err != nil {
			firstErr = err
		}
	}
	if c.Secondary != nil {
		inv, err := c.Secondary.Discover(ctx, t)
		if err == nil {
			return inv, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = errors.New("discovery: no usable data from BMC")
	}
	return inventory.Inventory{}, firstErr
}

// Default is the production discoverer: Redfish primary, IPMI fallback.
func Default() Discoverer {
	return Combined{Primary: RedfishDiscoverer{}, Secondary: IPMIDiscoverer{}}
}

// Simulated returns canned hardware facts without contacting a BMC — the
// discovery counterpart to the fake deploy executor. Used under --simulate so
// demos and the hermetic smoke test can register machines with no real IPMI.
type Simulated struct{}

func (Simulated) Discover(_ context.Context, t Target) (inventory.Inventory, error) {
	return inventory.Inventory{
		Source:       "simulated",
		Manufacturer: "Simulated",
		Model:        "cube-cos-driver-sim",
		Serial:       "SIM-" + t.Address,
		CPUModel:     "Simulated Xeon",
		CPUCount:     2,
		CPUCores:     32,
		MemoryBytes:  128 * 1024 * 1024 * 1024,
		NICs:         []inventory.NIC{{Name: "eth0", MAC: "02:00:00:00:00:01", SpeedMbps: 10000, Up: true}},
		Disks:        []inventory.Disk{{Name: "sda", Model: "SIM-SSD", SizeBytes: 480 * 1024 * 1024 * 1024, Type: "SSD"}},
	}, nil
}
