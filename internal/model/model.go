// Package model defines the clusterDetail JSON schema (unchanged from the
// legacy cube-snapshot-generator so old exports import cleanly).
package model

import (
	"errors"
	"fmt"
	"net"
)

var ErrInvalid = errors.New("invalid clusterDetail")

type IF struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"` // init|bond|vlan
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	IPAddr  string   `json:"IPAddr,omitempty"`
	IPMask  string   `json:"IPMask,omitempty"`
	Master  string   `json:"master,omitempty"`
	Slaves  []string `json:"slaves,omitempty"`
}

type IFInfo struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"`
}

func (i IFInfo) Empty() bool { return i.ID == "" }

type NodeRoleSettings struct {
	MgmtIF        IFInfo `json:"mgmtIF"`
	ProviderIF    IFInfo `json:"providerIF,omitempty"`
	OverlayIF     IFInfo `json:"overlayIF,omitempty"`
	StorIF        IFInfo `json:"storIF"`
	StorIFBackend IFInfo `json:"storIFBackend"`
}

type NodeConfig struct {
	ID             string           `json:"id"`
	Hostname       string           `json:"hostname"`
	InitIFs        []IF             `json:"initIFs"`
	BondIFs        []IF             `json:"bondIFs"`
	VlanIFs        []IF             `json:"vlanIFs"`
	DefaultIF      IFInfo           `json:"defaultIF"`
	DefaultGateway string           `json:"defaultGateway"`
	Role           string           `json:"role"`
	RoleSettings   NodeRoleSettings `json:"roleSettings"`
}

type Timezone struct {
	Name   string `json:"name"`
	Offset int    `json:"offset"`
}

type ClusterRoleSettings struct {
	ExtIP      string `json:"extIP"`
	Region     string `json:"region"`
	SecretSeed string `json:"secretSeed"`
	MgmtCIDR   string `json:"mgmtCIDR"`
}

type HASettings struct {
	VirtualIP       string `json:"virtualIP,omitempty"`
	VirtualHostname string `json:"virtualHostname,omitempty"`
}

type ClusterConfig struct {
	DNS          []string            `json:"DNS"`
	Timezone     Timezone            `json:"timezone"`
	RoleSettings ClusterRoleSettings `json:"roleSettings"`
	HA           bool                `json:"HA"`
	HASettings   HASettings          `json:"HASettings"`
	// SetReady carries the FTS-finalize parameters with the cluster
	// definition, so an exported/imported cluster keeps them (the live
	// trigger/ready state stays in the orchestrator store).
	SetReady *SetReadySettings `json:"setReady,omitempty"`
}

// SetReadySettings are the cluster set_ready (FTS finalize) parameters.
type SetReadySettings struct {
	CreateExternal bool   `json:"createExternal"`
	CIDR           string `json:"cidr"`
	Gateway        string `json:"gateway"`
	IPRange        string `json:"ipRange"`
}

type ClusterInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ClusterDetail struct {
	ClusterInfo   ClusterInfo   `json:"clusterInfo"`
	ClusterConfig ClusterConfig `json:"clusterConfig"`
	NodeData      []NodeConfig  `json:"nodeData"`
}

var Roles = []string{"control", "compute", "storage", "control-converged", "edge-core", "moderator"}

// RoleNodeIFs lists which role interfaces each role requires (legacy
// roleOptions.node). storIFBackend is always optional.
var RoleNodeIFs = map[string][]string{
	"control":           {"mgmtIF", "storIF"},
	"compute":           {"mgmtIF", "providerIF", "overlayIF", "storIF"},
	"storage":           {"mgmtIF", "storIF"},
	"control-converged": {"mgmtIF", "providerIF", "overlayIF", "storIF"},
	"edge-core":         {"mgmtIF", "providerIF", "overlayIF", "storIF"},
	"moderator":         {"mgmtIF", "storIF"},
}

func HasControlFunc(role string) bool {
	switch role {
	case "control", "control-converged", "edge-core", "moderator":
		return true
	}
	return false
}

func ValidRole(role string) bool {
	for _, r := range Roles {
		if r == role {
			return true
		}
	}
	return false
}

func (c ClusterDetail) ShortID() string {
	id := c.ClusterInfo.ID
	if len(id) < 12 {
		return id
	}
	return id[len(id)-12:]
}

func (n NodeConfig) AllIFs() []IF {
	ifs := make([]IF, 0, len(n.InitIFs)+len(n.BondIFs)+len(n.VlanIFs))
	ifs = append(ifs, n.InitIFs...)
	ifs = append(ifs, n.BondIFs...)
	ifs = append(ifs, n.VlanIFs...)
	return ifs
}

// IFName resolves an interface id to its label; "None" when missing (legacy
// IFidtoName behavior).
func (n NodeConfig) IFName(id string) string {
	for _, f := range n.AllIFs() {
		if f.ID == id {
			return f.Name
		}
	}
	return "None"
}

func (n NodeConfig) roleIF(key string) IFInfo {
	switch key {
	case "mgmtIF":
		return n.RoleSettings.MgmtIF
	case "providerIF":
		return n.RoleSettings.ProviderIF
	case "overlayIF":
		return n.RoleSettings.OverlayIF
	case "storIF":
		return n.RoleSettings.StorIF
	}
	return IFInfo{}
}

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func (c ClusterDetail) Validate() error {
	if len(c.ClusterInfo.ID) < 12 {
		return invalidf("cluster id %q must be at least 12 characters", c.ClusterInfo.ID)
	}
	if c.ClusterInfo.Name == "" {
		return invalidf("cluster name is empty")
	}
	if len(c.ClusterConfig.DNS) == 0 || c.ClusterConfig.DNS[0] == "" {
		return invalidf("at least one DNS server is required")
	}
	for _, d := range c.ClusterConfig.DNS {
		if net.ParseIP(d) == nil {
			return invalidf("DNS %q is not a valid IP", d)
		}
	}
	if c.ClusterConfig.HA {
		if c.ClusterConfig.HASettings.VirtualIP == "" || c.ClusterConfig.HASettings.VirtualHostname == "" {
			return invalidf("HA requires virtualIP and virtualHostname")
		}
	}
	if len(c.NodeData) == 0 {
		return invalidf("cluster has no nodes")
	}

	seen := map[string]bool{}
	hasControl := false
	needsControl := false
	for _, n := range c.NodeData {
		if n.Hostname == "" {
			return invalidf("node with empty hostname")
		}
		if seen[n.Hostname] {
			return invalidf("duplicate hostname %q", n.Hostname)
		}
		seen[n.Hostname] = true
		if !ValidRole(n.Role) {
			return invalidf("node %s: unknown role %q", n.Hostname, n.Role)
		}
		if HasControlFunc(n.Role) {
			hasControl = true
		} else {
			needsControl = true
		}
		for _, key := range RoleNodeIFs[n.Role] {
			info := n.roleIF(key)
			if info.Empty() || n.IFName(info.ID) == "None" {
				return invalidf("node %s: %s does not resolve to an interface", n.Hostname, key)
			}
		}
		if !n.RoleSettings.StorIFBackend.Empty() && n.IFName(n.RoleSettings.StorIFBackend.ID) == "None" {
			return invalidf("node %s: storIFBackend does not resolve to an interface", n.Hostname)
		}
	}
	if needsControl && !hasControl {
		return invalidf("compute/storage nodes present but no control-function node")
	}
	return nil
}
