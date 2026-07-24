package model

// PfLink is one interface the installer must stand up transiently to validate a
// node's network before it is reimaged.
type PfLink struct {
	Name    string   `json:"name"`              // IF label (e.g. IF.1, bond0, bond0.100)
	Type    string   `json:"type"`              // init|bond|vlan
	Members []string `json:"members,omitempty"` // physical member labels (bond)
	Parent  string   `json:"parent,omitempty"`  // parent link label (vlan)
	VLANID  int      `json:"vlanId,omitempty"`  // 802.1q tag (vlan)
	IP      string   `json:"ip,omitempty"`      // CIDR, e.g. 10.254.0.1/16
	Roles   []string `json:"roles,omitempty"`   // mgmt|provider|overlay|storage
	Gateway string   `json:"gateway,omitempty"` // set on the default interface
}

// PfPeer is one target the node pings from its own IP on the same role network.
type PfPeer struct {
	Hostname string `json:"hostname"`
	Role     string `json:"role"`
	IP       string `json:"ip"` // bare IP (no mask)
}

// PreflightBundle is the per-node network validation plan the SPA ships inside
// the snapshot; the installer agent configures Links and pings Peers (plus each
// link's gateway) before restore.
type PreflightBundle struct {
	Hostname string   `json:"hostname"`
	Links    []PfLink `json:"links"`
	Peers    []PfPeer `json:"peers"`
}
