package discovery

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	goipmi "github.com/bougou/go-ipmi"

	"github.com/bigstack-oss/cube-cos-snapshot/internal/inventory"
)

var errRedfishNoSystems = errors.New("redfish: no systems returned")

// IPMIDiscoverer reads FRU inventory (serial, board/product) over IPMI
// lanplus. It cannot enumerate NICs/disks/PCIe — fallback only.
type IPMIDiscoverer struct{}

func splitHostPort(addr string) (string, int) {
	port := 623
	host := addr
	if i := strings.LastIndex(addr, ":"); i > 0 && !strings.Contains(addr, "://") {
		if p, err := strconv.Atoi(addr[i+1:]); err == nil {
			host = addr[:i]
			port = p
		}
	}
	return host, port
}

func (IPMIDiscoverer) Discover(ctx context.Context, t Target) (inventory.Inventory, error) {
	host, port := splitHostPort(t.Address)
	client, err := goipmi.NewClient(host, port, t.Username, t.Password)
	if err != nil {
		return inventory.Inventory{}, err
	}
	client = client.WithInterface(goipmi.InterfaceLanplus)
	if err := client.Connect(ctx); err != nil {
		return inventory.Inventory{}, err
	}
	defer client.Close(ctx)

	fru, err := client.GetFRU(ctx, 0, "Builtin FRU")
	if err != nil {
		return inventory.Inventory{}, err
	}

	inv := inventory.Inventory{
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Source:    "ipmi",
	}
	if p := fru.ProductInfoArea; p != nil {
		inv.Manufacturer = fruStr(p.Manufacturer)
		inv.Model = fruStr(p.Name)
		inv.Serial = fruStr(p.SerialNumber)
	}
	if inv.Serial == "" {
		if b := fru.BoardInfoArea; b != nil {
			if inv.Manufacturer == "" {
				inv.Manufacturer = fruStr(b.Manufacturer)
			}
			if inv.Model == "" {
				inv.Model = fruStr(b.ProductName)
			}
			inv.Serial = fruStr(b.SerialNumber)
		}
	}
	return inv, nil
}

// fruStr cleans an IPMI FRU field: these are fixed-width and padded with trailing
// NUL bytes that TrimSpace leaves behind, which then break exact serial matching
// and show as garbage in the UI. Strip NULs, then trim surrounding whitespace.
func fruStr[T ~[]byte | ~string](v T) string {
	return strings.TrimSpace(strings.ReplaceAll(string(v), "\x00", ""))
}
