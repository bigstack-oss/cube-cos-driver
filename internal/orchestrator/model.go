// Package orchestrator drives zero-touch cluster deployment: per-node state
// machines take a node from power-on through PXE imaging (over IPMI), then the
// phone-home agent reports the snapshot apply. Hardware/agent I/O is behind
// interfaces so tests never touch a real BMC or node.
package orchestrator

import "strings"

type State string

const (
	StatePending      State = "pending"
	StateBMCPreflight State = "bmc-preflight"
	StateSetBootPXE   State = "set-boot-pxe"
	StatePowerCycle   State = "power-cycle"
	StateNetbooting   State = "netbooting"
	// Installer (pre-restore) phase, driven by `agent --preflight`.
	StatePreflighting State = "preflighting"  // configuring topology + pinging matrix
	StatePreflightOK  State = "preflight-ok"  // own matrix green; waiting for green light 1
	StateRestoring    State = "restoring"     // received GL1; imaging self
	StateRebooting    State = "rebooting"     // restore done; rebooting into installed OS
	StateImaging      State = "imaging"       // (legacy observe stage)
	StateImaged       State = "imaged"        // (legacy observe stage)
	StateCheckedIn    State = "checked-in"    // installed agent phoned home; waiting for GL2
	StateNetPreflight State = "net-preflight" // (legacy pre-apply gate)
	// StateWaiting: a non-master node has checked in but must wait for the
	// master node to finish its FTS before applying (green light 2).
	StateWaiting  State = "waiting-controller"
	StateApplying State = "applying"
	StateApplied  State = "applied"
	StateDone     State = "done"
	StateError    State = "error"
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

// NodePreflight is the installer-phase (pre-restore) validation outcome for one
// node: carrier on bond members, clock skew vs the server, and the peer/gateway
// ping matrix.
type NodePreflight struct {
	CarrierOK    bool              `json:"carrierOk"`
	ClockSkewSec float64           `json:"clockSkewSec"`
	Matrix       []PreflightResult `json:"matrix,omitempty"`
	Passed       bool              `json:"passed"`
	ReportedAt   string            `json:"reportedAt"`
}

type NodeDeploy struct {
	Hostname  string            `json:"hostname"`
	MachineID string            `json:"machineId"`
	State     State             `json:"state"`
	Message   string            `json:"message,omitempty"`
	ErrCode   ErrCode           `json:"errCode,omitempty"`
	Preflight []PreflightResult `json:"preflight,omitempty"`
	// InstallerPreflight is the pre-restore fabric-validation result (nil until
	// the installer agent reports).
	InstallerPreflight *NodePreflight `json:"installerPreflight,omitempty"`
	// RekickSeq increments when the operator asks this node's parked installer
	// agent to redo preflight from check-in (fresh bundle + snapshot) — no
	// PXE reboot. The agent sees it in every report response.
	RekickSeq int64  `json:"rekickSeq,omitempty"`
	UpdatedAt string `json:"updatedAt"`
	// Derived, read-only view for the UI (filled by snapshot()).
	Light1   Light       `json:"light1"`
	Light2   Light       `json:"light2"`
	Phase    Phase       `json:"phase"`
	Progress []PhaseCell `json:"progress"`
}

// PhaseCell is one segment of the per-node progress strip shown in the deploy
// UI: preflight → restore → reboot → apply, each pending → active → done/error.
type PhaseCell struct {
	Name   string     `json:"name"`
	Status CellStatus `json:"status"`
}

// CellStatus is the traffic-light state of a single progress cell.
type CellStatus string

const (
	CellPending CellStatus = "pending" // grey — not reached
	CellActive  CellStatus = "active"  // yellow — in progress
	CellDone    CellStatus = "done"    // green — complete
	CellError   CellStatus = "error"   // red — failed here
)

// Light is a traffic-light status for the two deploy gates.
type Light string

const (
	LightRed    Light = "red"    // failed
	LightYellow Light = "yellow" // in progress / waiting
	LightGreen  Light = "green"  // passed
	LightOff    Light = "off"    // not reached yet
)

// Phase is the coarse per-node stage shown in the deploy UI.
type Phase string

const (
	PhaseBoot         Phase = "boot"          // netbooting from the pxeserver
	PhasePreflightNet Phase = "preflight-net" // configuring topology + ping matrix
	PhaseTimeSync     Phase = "time"          // clock sync / skew gate
	PhaseWaitMaster   Phase = "wait-for-master"
	PhaseApplying     Phase = "applying" // applying snapshot / FTS
	PhaseDone         Phase = "done"
	PhaseError        Phase = "error"
)

// lights derives (light1, light2) from a node's state + preflight result.
// Light 1 = network preflight (fabric + carrier + skew). Light 2 = apply gate.
func (nd *NodeDeploy) lights() (Light, Light) {
	if nd.State == StateError {
		// A post-reboot apply failure is red on light 2 (light 1 already
		// passed). Everything earlier (BMC/PXE/preflight) is red on light 1.
		if strings.HasPrefix(string(nd.ErrCode), "APPLY_") {
			return LightGreen, LightRed
		}
		return LightRed, LightOff
	}
	l1 := LightOff
	switch nd.State {
	case StatePending, StateBMCPreflight, StateSetBootPXE, StatePowerCycle, StateNetbooting:
		l1 = LightOff
	case StatePreflighting:
		l1 = LightYellow
	default:
		l1 = LightGreen // preflight-ok and everything after it cleared GL1
	}
	l2 := LightOff
	switch nd.State {
	case StateCheckedIn, StateWaiting, StateNetPreflight:
		l2 = LightYellow
	case StateApplying, StateApplied:
		l2 = LightYellow
	case StateDone:
		l2 = LightGreen
	}
	return l1, l2
}

// phase derives the coarse UI phase from a node's state.
func (nd *NodeDeploy) phase() Phase {
	switch nd.State {
	case StatePending, StateBMCPreflight, StateSetBootPXE, StatePowerCycle, StateNetbooting, StateImaging, StateImaged:
		return PhaseBoot
	case StatePreflighting, StatePreflightOK, StateRestoring, StateRebooting:
		return PhasePreflightNet
	case StateCheckedIn, StateWaiting:
		return PhaseWaitMaster
	case StateNetPreflight, StateApplying, StateApplied:
		return PhaseApplying
	case StateDone:
		return PhaseDone
	case StateError:
		return PhaseError
	}
	return PhaseBoot
}

// pipelineRank places a state on the linear install pipeline so progress cells
// can classify each phase as pending/active/done.
func pipelineRank(s State) int {
	switch s {
	case StatePreflighting:
		return 1
	case StatePreflightOK:
		return 2
	case StateRestoring:
		return 3
	case StateRebooting:
		return 4
	case StateCheckedIn, StateWaiting, StateNetPreflight:
		return 5
	case StateApplying, StateApplied:
		return 6
	case StateDone:
		return 7
	default: // pending/bmc/pxe/power/netbooting/imaging
		return 0
	}
}

// progress builds the 4-cell per-node strip: preflight → restore → reboot →
// apply. A cell is done once the pipeline is past it, active while in it, and
// pending before. On error the failing cell is red and later cells stay pending.
func (nd *NodeDeploy) progress() []PhaseCell {
	cell := func(name string, activeRank, doneRank int) PhaseCell {
		r := pipelineRank(nd.State)
		st := CellPending
		if r >= doneRank {
			st = CellDone
		} else if r >= activeRank {
			st = CellActive
		}
		return PhaseCell{Name: name, Status: st}
	}
	// preflight active at rank 1, done at >=2; restore 3/>=4; reboot 4/>=5;
	// apply 5..6 active, done at 7.
	pre := cell("preflight", 1, 2)
	res := cell("restore", 3, 4)
	reb := cell("reboot", 4, 5)
	app := cell("apply", 5, 7)

	if nd.State == StateError {
		ec := string(nd.ErrCode)
		switch {
		case strings.HasPrefix(ec, "APPLY_"):
			pre.Status, res.Status, reb.Status, app.Status = CellDone, CellDone, CellDone, CellError
		case strings.HasPrefix(ec, "REBOOT_"):
			pre.Status, res.Status, reb.Status = CellDone, CellDone, CellError
		case strings.HasPrefix(ec, "RESTORE_"):
			pre.Status, res.Status = CellDone, CellError
		default: // BMC_/PXE_/PF_ — failed before restore
			pre.Status = CellError
		}
	}
	return []PhaseCell{pre, res, reb, app}
}

// isPreRestore reports whether a state is in the installer (pre-restore) phase.
func isPreRestore(s State) bool {
	switch s {
	case StatePending, StateBMCPreflight, StateSetBootPXE, StatePowerCycle,
		StateNetbooting, StatePreflighting, StatePreflightOK, StateRestoring, StateRebooting:
		return true
	}
	return false
}

type Deploy struct {
	ClusterID string `json:"clusterId"`
	StartedAt string `json:"startedAt"`
	// Master is the node whose FTS must complete before others apply.
	Master string                 `json:"master"`
	Nodes  map[string]*NodeDeploy `json:"nodes"` // by hostname
	// VerifyTargets are cluster-wide reachability targets (control VIP +
	// node mgmt IPs) tested only once every node reaches done.
	VerifyTargets []string `json:"verifyTargets,omitempty"`
	// ClusterReady + Verify hold the Tier-2 whole-cluster check result.
	ClusterReady bool              `json:"clusterReady"`
	Verify       []PreflightResult `json:"verify,omitempty"`
	// Manual gates the deploy step-by-step: the operator advances ManualStep
	// via AdvanceStep, and each phase barrier holds until the step reaches it.
	// 1=preflight 2=restore 3=reboot 4=apply-master 5=apply-rest+set_ready.
	Manual     bool `json:"manual"`
	ManualStep int  `json:"manualStep,omitempty"`
	// CanAdvance (computed) reports whether the current manual step's nodes have
	// reached its state, so the UI can gate the operator's Next button.
	CanAdvance bool `json:"canAdvance"`
}

// Manual-deploy step cursor values (Deploy.ManualStep).
const (
	StepPreflight   = 1 // preflight running; Next authorizes restore
	StepRestore     = 2 // restore authorized; Next authorizes reboot
	StepReboot      = 3 // reboot authorized; Next authorizes master apply
	StepApplyMaster = 4 // master apply authorized; Next authorizes apply-rest+set_ready
	StepApplyRest   = 5 // peers apply + set_ready authorized (final)
)

// terminal reports whether a node state needs no further engine stepping.
func terminal(s State) bool {
	return s == StateError || s == StateDone
}
