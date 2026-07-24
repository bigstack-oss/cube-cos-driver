// Package orchestrator drives zero-touch cluster deployment: per-node state
// machines take a node from power-on through PXE imaging (over IPMI), then the
// phone-home agent reports the snapshot apply. Hardware/agent I/O is behind
// interfaces so tests never touch a real BMC or node.
package orchestrator

type State string

const (
	StatePending      State = "pending"
	StateBMCPreflight State = "bmc-preflight"
	StateSetBootPXE   State = "set-boot-pxe"
	StatePowerCycle   State = "power-cycle"
	StateNetbooting   State = "netbooting"
	StateImaging      State = "imaging"
	StateImaged       State = "imaged"
	StateCheckedIn    State = "checked-in"
	// StateWaiting: a non-master node has checked in but must wait for the
	// master node to finish its FTS before applying its own snapshot.
	StateWaiting      State = "waiting-controller"
	StateNetPreflight State = "net-preflight"
	StateApplying     State = "applying"
	StateApplied      State = "applied"
	StateDone         State = "done"
	StateError        State = "error"
)

// Stage is the coarse install progress observed on the pxeserver.
type Stage string

const (
	StageNone    Stage = "none"
	StageDHCP    Stage = "dhcp"    // node's MAC took a DHCP lease
	StageImaging Stage = "imaging" // node is fetching the install media
	StageDone    Stage = "done"    // media fully fetched
)

// Node is the executor's view of one node to deploy.
type Node struct {
	Hostname   string   `json:"hostname"`
	MachineID  string   `json:"machineId"`
	BMCAddress string   `json:"bmcAddress"`
	BMCUser    string   `json:"bmcUser"`
	BMCPass    string   `json:"-"`
	MACs       []string `json:"macs"`
	OSDisk     string   `json:"osDisk"`
}

// PreflightResult is one target's connectivity/time check from the agent.
type PreflightResult struct {
	Target string `json:"target"` // e.g. "gateway 10.0.0.254" or "time skew"
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type NodeDeploy struct {
	Hostname  string            `json:"hostname"`
	MachineID string            `json:"machineId"`
	State     State             `json:"state"`
	Message   string            `json:"message,omitempty"`
	Preflight []PreflightResult `json:"preflight,omitempty"`
	UpdatedAt string            `json:"updatedAt"`
}

type Deploy struct {
	ClusterID string `json:"clusterId"`
	StartedAt string `json:"startedAt"`
	// Master is the node whose FTS must complete before others apply.
	Master string                 `json:"master"`
	Nodes  map[string]*NodeDeploy `json:"nodes"` // by hostname
}

// terminal reports whether a node state needs no further engine stepping.
func terminal(s State) bool {
	return s == StateError || s == StateDone
}
