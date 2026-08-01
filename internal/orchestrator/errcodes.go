package orchestrator

// ErrCode is a stage-specific machine-readable failure code, surfaced in the
// deploy UI so an operator can tell *where* and *why* a node failed. The
// prefix identifies the stage: BMC_/PXE_ (orchestrator, pre-boot), PF_
// (installer preflight, green light 1), APPLY_ (post-reboot, green light 2).
type ErrCode string

const (
	// BMC / power (orchestrator-driven).
	ErrBMCUnreachable ErrCode = "BMC_UNREACHABLE" // IPMI LAN not answering
	ErrBMCAuth        ErrCode = "BMC_AUTH"        // bad credentials
	ErrBMCBootdev     ErrCode = "BMC_BOOTDEV"     // set one-time PXE failed
	ErrBMCPower       ErrCode = "BMC_POWER"       // power on/cycle failed

	// PXE / netboot.
	ErrPXENoLease ErrCode = "PXE_NO_LEASE" // no DHCP lease for the node's MACs
	ErrPXETimeout ErrCode = "PXE_TIMEOUT"  // install media never fully fetched

	// Preflight (installer, green light 1).
	ErrPFCarrier ErrCode = "PF_CARRIER"    // a bond member has no carrier (degraded)
	ErrPFPing    ErrCode = "PF_PING"       // a peer or gateway is unreachable
	ErrPFSkew    ErrCode = "PF_CLOCK_SKEW" // clock skew exceeds the ±5s gate
	ErrPFTopo    ErrCode = "PF_TOPOLOGY"   // failed to configure bond/vlan/ip
	ErrPFTimeout ErrCode = "PF_TIMEOUT"    // matrix never converged

	// Apply (post-reboot, green light 2).
	ErrApplyDownload ErrCode = "APPLY_DOWNLOAD" // snapshot fetch failed
	ErrApplyFailed   ErrCode = "APPLY_FAILED"   // hex_config snapshot_apply failed
	ErrApplyTimeout  ErrCode = "APPLY_TIMEOUT"  // FTS never completed

	ErrCancelled ErrCode = "CANCELLED" // deploy cancelled by the operator

	ErrInternal ErrCode = "INTERNAL"
)
