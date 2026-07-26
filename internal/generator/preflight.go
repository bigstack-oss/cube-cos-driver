package generator

import (
	"net"
	"strconv"
	"strings"

	"github.com/bigstack-oss/cube-cos-driver/internal/model"
)

// preflightRoles are the role networks whose IPs the installer preflight
// configures and pings (in a stable order). storIFBackend is validated with
// storIF.
var preflightRoles = []string{"mgmt", "provider", "overlay", "storage"}

// maskToPrefix converts a dotted netmask ("255.255.0.0") to a prefix length.
// Returns -1 if the mask is empty or unparseable.
func maskToPrefix(mask string) int {
	ip := net.ParseIP(strings.TrimSpace(mask))
	if ip == nil {
		return -1
	}
	if v4 := ip.To4(); v4 != nil {
		ones, _ := net.IPv4Mask(v4[0], v4[1], v4[2], v4[3]).Size()
		return ones
	}
	return -1
}

// cidr joins an address and dotted mask into "ip/prefix"; falls back to the
// bare IP when the mask is missing.
func cidr(ip, mask string) string {
	if ip == "" {
		return ""
	}
	if p := maskToPrefix(mask); p >= 0 {
		return ip + "/" + strconv.Itoa(p)
	}
	return ip
}

// vlanTag extracts the 802.1q tag from a vlan interface label ("bond0.100" →
// 100); 0 when absent.
func vlanTag(name string) int {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return 0
	}
	tag, err := strconv.Atoi(name[i+1:])
	if err != nil {
		return 0
	}
	return tag
}

// roleIFIDs maps each preflight role to the interface id it resolves to for a
// node (empty when the role has no interface).
func roleIFIDs(n model.NodeConfig) map[string]string {
	rs := n.RoleSettings
	return map[string]string{
		"mgmt":     rs.MgmtIF.ID,
		"provider": rs.ProviderIF.ID,
		"overlay":  rs.OverlayIF.ID,
		"storage":  rs.StorIF.ID,
	}
}

// roleIP returns the node's IP (bare, no mask) on the given role network, or ""
// if the role has no enabled interface with an address.
func roleIP(n model.NodeConfig, role string) string {
	id := roleIFIDs(n)[role]
	if id == "" {
		return ""
	}
	for _, f := range n.AllIFs() {
		if f.ID == id {
			return f.IPAddr
		}
	}
	return ""
}

// BuildPreflightBundles derives one PreflightBundle per node: the transient
// topology to configure and the cross-node peer matrix to ping. The SPA already
// holds every node's config, so the matrix is fully known.
func BuildPreflightBundles(d model.ClusterDetail) map[string]model.PreflightBundle {
	out := make(map[string]model.PreflightBundle, len(d.NodeData))
	for _, n := range d.NodeData {
		out[n.Hostname] = model.PreflightBundle{
			Hostname: n.Hostname,
			Links:    buildLinks(n),
			Peers:    buildPeers(n, d.NodeData),
		}
	}
	return out
}

func buildLinks(n model.NodeConfig) []model.PfLink {
	// Which roles each interface id serves (an id may serve several).
	rolesByIF := map[string][]string{}
	ids := roleIFIDs(n)
	for _, role := range preflightRoles {
		if id := ids[role]; id != "" {
			rolesByIF[id] = append(rolesByIF[id], role)
		}
	}
	var links []model.PfLink
	for _, f := range n.AllIFs() {
		if !f.Enabled {
			continue
		}
		l := model.PfLink{Name: f.Name, Type: f.Type, IP: cidr(f.IPAddr, f.IPMask), Roles: rolesByIF[f.ID]}
		switch f.Type {
		case "bond":
			for _, sid := range f.Slaves {
				l.Members = append(l.Members, n.IFName(sid))
			}
		case "vlan":
			if f.Master != "" {
				l.Parent = n.IFName(f.Master)
			}
			l.VLANID = vlanTag(f.Name)
		}
		if f.ID == n.DefaultIF.ID {
			l.Gateway = n.DefaultGateway
		}
		links = append(links, l)
	}
	return links
}

func buildPeers(self model.NodeConfig, all []model.NodeConfig) []model.PfPeer {
	var peers []model.PfPeer
	for _, role := range preflightRoles {
		if roleIP(self, role) == "" {
			continue // this node isn't on that network
		}
		for _, other := range all {
			if other.Hostname == self.Hostname {
				continue
			}
			if ip := roleIP(other, role); ip != "" {
				peers = append(peers, model.PfPeer{Hostname: other.Hostname, Role: role, IP: ip})
			}
		}
	}
	return peers
}
