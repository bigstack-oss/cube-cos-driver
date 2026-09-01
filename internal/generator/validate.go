package generator

import (
	"fmt"
	"net"
	"strings"

	"github.com/bigstack-oss/cube-cos-driver/internal/model"
)

// ValidateNodeNetwork rejects an interface config that would render an invalid
// static IPv4 address. An enabled interface carrying an IP but no (or a zero)
// netmask used to render as netmask 0.0.0.0 — a /0 mask that leaves the node
// with no usable subnet, an unreachable gateway, and a failed FTS. Catch it
// here so a bad config fails at save/deploy instead of on the node.
func ValidateNodeNetwork(n model.NodeConfig) error {
	var all []model.IF
	all = append(all, n.InitIFs...)
	all = append(all, n.BondIFs...)
	all = append(all, n.VlanIFs...)
	for _, f := range all {
		if !f.Enabled {
			continue
		}
		addr := strings.TrimSpace(f.IPAddr)
		mask := strings.TrimSpace(f.IPMask)
		if addr == "" && mask == "" {
			continue // no static IPv4 on this interface — nothing to validate
		}
		if ip := net.ParseIP(addr); ip == nil || ip.To4() == nil {
			return fmt.Errorf("interface %s: invalid or missing IPv4 address %q", f.Name, f.IPAddr)
		}
		if !validIPv4Mask(mask) {
			return fmt.Errorf("interface %s: invalid or missing netmask %q — set a mask such as 255.255.0.0", f.Name, f.IPMask)
		}
	}
	return nil
}

// validIPv4Mask reports whether mask is a canonical, non-zero dotted IPv4
// netmask (e.g. 255.255.0.0). Rejects "", "0.0.0.0", and non-contiguous masks.
func validIPv4Mask(mask string) bool {
	ip := net.ParseIP(mask)
	if ip == nil || ip.To4() == nil {
		return false
	}
	v4 := ip.To4()
	ones, bits := net.IPv4Mask(v4[0], v4[1], v4[2], v4[3]).Size()
	// Size() returns (0,0) for a non-contiguous mask and (0,32) for 0.0.0.0.
	return bits == 32 && ones > 0
}
