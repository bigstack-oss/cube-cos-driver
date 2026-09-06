// Package enterprise builds and drives the install plan for app-framework 2 and cube-cmp 2.1.0.
package enterprise

// Module identifiers for the enterprise install flow.
const (
	ModuleAppFW   = "appfw"
	ModuleCMP     = "cmp"
	ModuleAdvisor = "advisor"
)

// StepState is the lifecycle state of a persisted Step.
type StepState string

// Step states.
const (
	StepPending StepState = "pending"
	StepActive  StepState = "active"
	StepDone    StepState = "done"
	StepError   StepState = "error"
	StepSkipped StepState = "skipped"
)

// InstallParams holds the user-supplied and derived parameters for an install run.
type InstallParams struct {
	Project   string // framework_create project name
	PublicNet string // default "public"
	MgmtNet   string // default "public"
	LBIP      string
	OSImage   string // rancher .raw basename (no path)
	Framework string // CMP: target framework (may equal Project when auto-created)
	AppFile   string // CMP: cube-portal-*.pigz basename
	FsImage   string // manila-*.qcow2 basename
	LBImage   string // amphora-*.qcow2 basename

	AdvisorFile string // advisor: cube-advisor-*.pigz basename
	AdvisorLBIP string // advisor: dedicated LoadBalancer IP for the advisor service
	// AdvisorBaseURL is the origin a browser reaches the advisor on, which the
	// service uses to build its OAuth redirect_uri. Defaults to
	// http://<AdvisorLBIP>/ when empty.
	//
	// It is a separate field from AdvisorLBIP because the two genuinely differ
	// whenever the advisor is not reached at its LoadBalancer address directly:
	// behind a TLS terminator it is https://<name>, and through an SSH tunnel it
	// is http://localhost:<port>. The distinction matters more than it looks —
	// the session cookie is __Host- prefixed and Secure, and browsers store
	// Secure cookies only on a secure context, so plain HTTP on a bare lab IP
	// cannot complete a sign-in at all while localhost and HTTPS both can.
	AdvisorBaseURL string

	StorageBackend string // cinder volume type for image import; from cluster query
}

// Step is the persisted, user-visible record of one install step.
type Step struct {
	Name, Title string
	State       StepState
	Output      string
	Err         string
	StartedAt   string // RFC3339, stamped when the step goes active
	FinishedAt  string // RFC3339, stamped when the step reaches done/skipped/error
}

// Install is the persisted record of one install run for a cluster+module.
type Install struct {
	ClusterID      string
	Module         string
	Op             string // "install" (default) | "uninstall"
	Host           string // the dialed target (cluster VIP or ad-hoc VIP); cascade identity
	StartedAt      string
	Manual         bool
	ManualStep     int
	SimulateAirgap bool
	Params         InstallParams
	Steps          []*Step
	Current        int
	State          string // "running" | "done" | "error"
	Portal         string
}

// plannedStep is the executable form of a step (not persisted; rebuilt from params).
type plannedStep struct {
	Name, Title, Kind string // Kind: "run" | "scp+run" | "airgap" | "framework" | "detect" | "complete"
	Cmd               string // for run / scp+run / framework
	LocalPath         string // for scp+run: <DataDir>/enterprise/.../<file>
	RemotePath        string // for scp+run: cephfs dir
	ImageName         string // for scp+run image imports: glance image name to idempotency-check
	Framework         string // for framework: the app-framework name to create + poll to active
	LBIP              string // for framework: the ingress LB IP, to verify registry reachability
}
