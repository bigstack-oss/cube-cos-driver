// Package generator renders hex policy YAMLs and assembles .snapshot zips,
// byte-compatible with the legacy JS implementation (see golden tests).
package generator

import (
	"strconv"
	"strings"

	"github.com/bigstack-oss/cube-cos-snapshot/internal/model"
)

type ControlInfo struct {
	Hostnames []string
	IPs       []string
}

// GetControlInfo collects mgmt IPs/hostnames of control-function nodes in
// nodeData order (legacy getControlInfo).
func GetControlInfo(nodes []model.NodeConfig) ControlInfo {
	var ctl ControlInfo
	for _, n := range nodes {
		if !model.HasControlFunc(n.Role) {
			continue
		}
		ip := "None"
		for _, f := range n.AllIFs() {
			if f.ID == n.RoleSettings.MgmtIF.ID && f.IPAddr != "" {
				ip = f.IPAddr
				break
			}
		}
		ctl.Hostnames = append(ctl.Hostnames, n.Hostname)
		ctl.IPs = append(ctl.IPs, ip)
	}
	return ctl
}

// clusterOptions / nodeOptions mirror legacy roleOptions ordering.
var clusterOptions = map[string][]string{
	"control":           {"extIP", "region", "secretSeed", "mgmtCIDR"},
	"compute":           {"ctrlHostname", "ctrlIP", "extIP", "region", "secretSeed"},
	"storage":           {"ctrlHostname", "ctrlIP", "region", "secretSeed"},
	"control-converged": {"extIP", "region", "secretSeed", "mgmtCIDR"},
	"edge-core":         {"extIP", "region", "secretSeed", "mgmtCIDR"},
	"moderator":         {"extIP", "region", "secretSeed", "mgmtCIDR"},
}

var nodeOptions = map[string][]string{
	"control":           {"mgmtIF", "storIF"},
	"compute":           {"mgmtIF", "providerIF", "overlayIF", "storIF"},
	"storage":           {"mgmtIF", "storIF"},
	"control-converged": {"mgmtIF", "providerIF", "overlayIF", "storIF"},
	"edge-core":         {"mgmtIF", "providerIF", "overlayIF", "storIF"},
	"moderator":         {"mgmtIF", "storIF"},
}

func RenderCubesys(n model.NodeConfig, cc model.ClusterConfig, ctl ControlInfo) string {
	d := &doc{}
	d.header("cubesys/cubesys1_0.yml")
	d.kv(0, "name", "cubesys")
	d.kv(0, "version", "1.0")
	d.kv(0, "role", n.Role)
	d.kv(0, "domain", "default")
	d.kv(0, "ha", cc.HA)
	d.kv(0, "saltkey", true)

	// controller: initial value, possibly overwritten by ctrlHostname (key
	// keeps its insertion position, matching the legacy JS object).
	var controller any
	if cc.HA {
		controller = cc.HASettings.VirtualHostname
	}
	for _, opt := range clusterOptions[n.Role] {
		if opt == "ctrlHostname" {
			if cc.HA {
				controller = cc.HASettings.VirtualHostname
			} else if len(ctl.Hostnames) > 0 {
				controller = ctl.Hostnames[0]
			}
		}
	}
	d.kv(0, "controller", controller)

	for _, opt := range clusterOptions[n.Role] {
		switch opt {
		case "ctrlIP":
			if cc.HA {
				d.kv(0, "controller-ip", cc.HASettings.VirtualIP)
			} else if len(ctl.IPs) > 0 {
				d.kv(0, "controller-ip", ctl.IPs[0])
			} else {
				d.kv(0, "controller-ip", nil)
			}
		case "extIP":
			d.kv(0, "external", cc.RoleSettings.ExtIP)
		case "region":
			d.kv(0, "region", cc.RoleSettings.Region)
		case "secretSeed":
			d.kv(0, "secret-seed", cc.RoleSettings.SecretSeed)
		case "mgmtCIDR":
			d.kv(0, "mgmt-cidr", cc.RoleSettings.MgmtCIDR)
		}
	}

	for _, opt := range nodeOptions[n.Role] {
		switch opt {
		case "mgmtIF":
			d.kv(0, "management", n.IFName(n.RoleSettings.MgmtIF.ID))
		case "providerIF":
			d.kv(0, "provider", n.IFName(n.RoleSettings.ProviderIF.ID))
		case "overlayIF":
			d.kv(0, "overlay", n.IFName(n.RoleSettings.OverlayIF.ID))
		case "storIF":
			storage := n.IFName(n.RoleSettings.StorIF.ID)
			if !n.RoleSettings.StorIFBackend.Empty() {
				storage += "," + n.IFName(n.RoleSettings.StorIFBackend.ID)
			}
			d.kv(0, "storage", storage)
		}
	}

	if cc.HA {
		if model.HasControlFunc(n.Role) {
			d.kv(0, "control-vip", cc.HASettings.VirtualIP)
		}
		d.kv(0, "control-hosts", strings.Join(ctl.Hostnames, ","))
		d.kv(0, "control-addrs", strings.Join(ctl.IPs, ","))
	}
	return d.String()
}

// ifType mirrors legacy getIFtype: vlan→3, bond→1, init without master→0,
// init with master (bond slave)→2.
func ifType(f model.IF) int {
	switch {
	case f.Type == "vlan":
		return 3
	case f.Type == "bond":
		return 1
	case f.Type == "init" && f.Master == "":
		return 0
	}
	return 2
}

func RenderNetwork(n model.NodeConfig, cc model.ClusterConfig) string {
	d := &doc{}
	d.header("network/network1_0.yml")
	d.kv(0, "name", "network")
	d.kv(0, "version", "1.0")
	d.kv(0, "hostname", n.Hostname)
	d.kv(0, "default-interface", n.IFName(n.DefaultIF.ID))
	d.raw(0, "dns:")
	d.kv(2, "auto", false)
	if len(cc.DNS) > 0 {
		d.kv(2, "primary", cc.DNS[0])
	} else {
		d.kv(2, "primary", nil)
	}
	if len(cc.DNS) > 1 {
		d.kv(2, "secondary", cc.DNS[1])
	}
	d.raw(0, "interfaces:")
	for _, f := range n.AllIFs() {
		d.raw(2, "- type: "+strconv.Itoa(ifType(f)))
		d.kv(4, "enabled", f.Enabled)
		d.kv(4, "label", f.Name)
		if f.Master != "" {
			d.kv(4, "master", n.IFName(f.Master))
		}
		d.kv(4, "speed-duplex", "auto")
		d.raw(4, "ipv4:")
		d.kv(6, "dhcp", !f.Enabled)
		if f.Enabled {
			d.kv(6, "ipaddr", orDefault(f.IPAddr, "0.0.0.0"))
			d.kv(6, "netmask", orDefault(f.IPMask, "0.0.0.0"))
			if f.ID == n.DefaultIF.ID {
				d.kv(6, "gateway", n.DefaultGateway)
			} else {
				d.kv(6, "gateway", nil)
			}
		}
		d.raw(4, "ipv6:")
		d.kv(6, "enabled", true)
		d.kv(6, "dhcp", true)
	}
	return d.String()
}

func RenderTime(cc model.ClusterConfig) string {
	d := &doc{}
	d.header("time/time1_0.yml")
	d.kv(0, "name", "time")
	d.kv(0, "version", "1.0")
	d.kv(0, "timezone", cc.Timezone.Name)
	return d.String()
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
