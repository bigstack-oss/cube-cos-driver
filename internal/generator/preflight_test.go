package generator

import (
	"testing"

	"github.com/bigstack-oss/cube-cos-driver/internal/model"
)

func TestMaskToPrefixAndCIDR(t *testing.T) {
	cases := map[string]int{"255.255.0.0": 16, "255.255.255.0": 24, "255.255.255.255": 32, "": -1, "bogus": -1}
	for mask, want := range cases {
		if got := maskToPrefix(mask); got != want {
			t.Errorf("maskToPrefix(%q)=%d want %d", mask, got, want)
		}
	}
	if got := cidr("10.254.0.1", "255.255.0.0"); got != "10.254.0.1/16" {
		t.Errorf("cidr=%q", got)
	}
	if got := cidr("10.0.0.5", ""); got != "10.0.0.5" {
		t.Errorf("cidr no mask=%q", got)
	}
	if got := cidr("", "255.0.0.0"); got != "" {
		t.Errorf("cidr no ip=%q", got)
	}
}

func TestVlanTag(t *testing.T) {
	for name, want := range map[string]int{"bond0.100": 100, "IF.3": 3, "bond0": 0, "eth0.4094": 4094} {
		if got := vlanTag(name); got != want {
			t.Errorf("vlanTag(%q)=%d want %d", name, got, want)
		}
	}
}

// twoNode builds a minimal two-node cluster sharing a mgmt network, where each
// node's mgmt/overlay/storage ride IF.1 and provider rides a disabled IF.2.
func twoNode() model.ClusterDetail {
	mk := func(host, ip string) model.NodeConfig {
		return model.NodeConfig{
			Hostname: host,
			InitIFs: []model.IF{
				{ID: host + "-1", Type: "init", Name: "IF.1", Enabled: true, IPAddr: ip, IPMask: "255.255.0.0"},
				{ID: host + "-2", Type: "init", Name: "IF.2", Enabled: false},
			},
			DefaultIF:      model.IFInfo{ID: host + "-1"},
			DefaultGateway: "10.254.0.254",
			Role:           "control-converged",
			RoleSettings: model.NodeRoleSettings{
				MgmtIF:    model.IFInfo{ID: host + "-1"},
				OverlayIF: model.IFInfo{ID: host + "-1"},
				StorIF:    model.IFInfo{ID: host + "-1"},
			},
		}
	}
	return model.ClusterDetail{NodeData: []model.NodeConfig{mk("cube-1", "10.254.0.1"), mk("cube-2", "10.254.0.2")}}
}

func TestBuildPreflightBundles(t *testing.T) {
	bundles := BuildPreflightBundles(twoNode())
	if len(bundles) != 2 {
		t.Fatalf("want 2 bundles, got %d", len(bundles))
	}
	b := bundles["cube-1"]

	// Only the enabled IF.1 becomes a link; it carries the mgmt/overlay/storage
	// roles, the CIDR, and the gateway (it is the default interface).
	if len(b.Links) != 1 {
		t.Fatalf("want 1 enabled link, got %d: %+v", len(b.Links), b.Links)
	}
	l := b.Links[0]
	if l.IP != "10.254.0.1/16" {
		t.Errorf("link IP = %q", l.IP)
	}
	if l.Gateway != "10.254.0.254" {
		t.Errorf("link gateway = %q", l.Gateway)
	}
	wantRoles := map[string]bool{"mgmt": true, "overlay": true, "storage": true}
	if len(l.Roles) != 3 {
		t.Errorf("roles = %v", l.Roles)
	}
	for _, r := range l.Roles {
		if !wantRoles[r] {
			t.Errorf("unexpected role %q", r)
		}
	}

	// cube-1 pings cube-2 once per shared role network (mgmt, overlay, storage).
	if len(b.Peers) != 3 {
		t.Fatalf("want 3 peer entries, got %d: %+v", len(b.Peers), b.Peers)
	}
	for _, p := range b.Peers {
		if p.Hostname != "cube-2" || p.IP != "10.254.0.2" {
			t.Errorf("bad peer %+v", p)
		}
	}
}
