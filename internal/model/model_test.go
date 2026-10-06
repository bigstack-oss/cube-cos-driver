package model

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func loadHA3(t *testing.T) ClusterDetail {
	t.Helper()
	raw, err := os.ReadFile("testdata/ha3.json")
	if err != nil {
		t.Fatal(err)
	}
	var d ClusterDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestHA3Valid(t *testing.T) {
	d := loadHA3(t)
	if err := d.Validate(); err != nil {
		t.Fatalf("expected valid: %v", err)
	}
	if got := d.ShortID(); len(got) != 12 || got != "aabbccddee01" {
		t.Fatalf("ShortID = %q", got)
	}
}

func TestIFNameResolution(t *testing.T) {
	d := loadHA3(t)
	n := d.NodeData[1] // cube-2 with bond+vlan
	if got := n.IFName("aaaa0002-0000-0000-0000-000000000010"); got != "bond0" {
		t.Fatalf("bond name = %q", got)
	}
	if got := n.IFName("nope"); got != "None" {
		t.Fatalf("missing IF = %q", got)
	}
}

func TestValidateFailures(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ClusterDetail)
	}{
		{"dup hostname", func(d *ClusterDetail) { d.NodeData[1].Hostname = d.NodeData[0].Hostname }},
		{"missing mgmtIF", func(d *ClusterDetail) { d.NodeData[0].RoleSettings.MgmtIF = IFInfo{} }},
		{"dangling mgmtIF", func(d *ClusterDetail) { d.NodeData[0].RoleSettings.MgmtIF.ID = "nope" }},
		{"ha without vip", func(d *ClusterDetail) { d.ClusterConfig.HASettings.VirtualIP = "" }},
		{"bad role", func(d *ClusterDetail) { d.NodeData[0].Role = "worker" }},
		{"no dns", func(d *ClusterDetail) { d.ClusterConfig.DNS = nil }},
		{"bad dns ip", func(d *ClusterDetail) { d.ClusterConfig.DNS = []string{"not-an-ip"} }},
		{"short id", func(d *ClusterDetail) { d.ClusterInfo.ID = "short" }},
		{"no nodes", func(d *ClusterDetail) { d.NodeData = nil }},
		{"mgmtCIDR overlaps node subnet", func(d *ClusterDetail) { d.ClusterConfig.RoleSettings.MgmtCIDR = "10.254.0.0/16" }},
		{"mgmtCIDR contains gateway", func(d *ClusterDetail) {
			d.ClusterConfig.RoleSettings.MgmtCIDR = "10.9.0.0/16"
			d.NodeData[0].DefaultGateway = "10.9.0.1"
		}},
		{"mgmtCIDR contains DNS", func(d *ClusterDetail) { d.ClusterConfig.DNS = []string{"10.100.0.53"} }},
		{"mgmtCIDR contains VIP", func(d *ClusterDetail) { d.ClusterConfig.HASettings.VirtualIP = "10.100.1.1" }},
		{"mgmtCIDR overlaps set-ready cidr", func(d *ClusterDetail) {
			d.ClusterConfig.SetReady = &SetReadySettings{CreateExternal: true, CIDR: "10.100.0.0/24"}
		}},
		{"mgmtCIDR not /16 or /8", func(d *ClusterDetail) { d.ClusterConfig.RoleSettings.MgmtCIDR = "10.100.0.0/24" }},
		{"mgmtCIDR not a CIDR", func(d *ClusterDetail) { d.ClusterConfig.RoleSettings.MgmtCIDR = "10.100.0.0" }},
		{"compute without control", func(d *ClusterDetail) {
			for i := range d.NodeData {
				d.NodeData[i].Role = "compute"
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := loadHA3(t)
			tc.mutate(&d)
			err := d.Validate()
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("not ErrInvalid: %v", err)
			}
		})
	}
}

func TestJSONRoundTrip(t *testing.T) {
	d := loadHA3(t)
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var d2 ClusterDetail
	if err := json.Unmarshal(out, &d2); err != nil {
		t.Fatal(err)
	}
	if d2.NodeData[1].BondIFs[0].Slaves[1] != d.NodeData[1].BondIFs[0].Slaves[1] {
		t.Fatal("round trip lost bond slaves")
	}
}

func TestMgmtCIDREmptyUsesCubeDefault(t *testing.T) {
	d := loadHA3(t)
	d.ClusterConfig.RoleSettings.MgmtCIDR = ""
	if err := d.Validate(); err != nil {
		t.Fatalf("empty mgmtCIDR should be accepted: %v", err)
	}
}
