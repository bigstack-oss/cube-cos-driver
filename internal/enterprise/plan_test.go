package enterprise

import (
	"reflect"
	"strings"
	"testing"
)

func names(ps []plannedStep) []string {
	var n []string
	for _, s := range ps {
		n = append(n, s.Name)
	}
	return n
}

func TestBuildPlan_AppFW(t *testing.T) {
	p := InstallParams{Project: "cmp", PublicNet: "public", MgmtNet: "public", LBIP: "10.32.36.120",
		OSImage: "rancher-cluster-image-rke2-v1.32.4.raw", FsImage: "manila-service-image-yoga.qcow2", LBImage: "amphora-x64-haproxy-yoga.qcow2"}
	got := names(BuildPlan(ModuleAppFW, p, false, "/data", nil))
	want := []string{"preflight", "import_fs", "import_lb", "import", "framework_create"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
func TestBuildPlan_AppFW_Airgap(t *testing.T) {
	got := names(BuildPlan(ModuleAppFW, InstallParams{}, true, "/data", nil))
	if got[0] != "preflight" || got[1] != "airgap-apply" {
		t.Fatalf("airgap not after preflight: %v", got)
	}
}
func TestBuildPlan_CMP_AlwaysRunsAppFWFirst(t *testing.T) {
	// The App-Framework sequence is always in the CMP plan; framework_create is
	// idempotent (skips/waits when the framework is already active).
	got := names(BuildPlan(ModuleCMP, InstallParams{}, false, "/data", nil))
	want := []string{"preflight", "import_fs", "import_lb", "import", "framework_create", "app_register", "install_portal", "complete"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
func TestBuildPlan_FrameworkCreate_IsFrameworkKind(t *testing.T) {
	p := InstallParams{Project: "cmp", PublicNet: "public", MgmtNet: "public", LBIP: "10.32.36.120",
		OSImage: "rancher-cluster-image-rke2-v1.32.4.raw"}
	for _, s := range BuildPlan(ModuleAppFW, p, false, "/data", nil) {
		if s.Name == "framework_create" {
			if s.Kind != "framework" {
				t.Fatalf("framework_create kind = %q, want framework", s.Kind)
			}
			if s.Framework != "cmp" {
				t.Fatalf("framework_create Framework = %q, want cmp", s.Framework)
			}
		}
	}
}
func TestBuildPlan_FrameworkCreateCmd_UsesImageNameNoExt(t *testing.T) {
	p := InstallParams{Project: "cmp", PublicNet: "public", MgmtNet: "public", LBIP: "10.32.36.120", OSImage: "rancher-cluster-image-rke2-v1.32.4.raw"}
	ps := BuildPlan(ModuleAppFW, p, false, "/data", nil)
	var fc plannedStep
	for _, s := range ps {
		if s.Name == "framework_create" {
			fc = s
		}
	}
	if !strings.Contains(fc.Cmd, "rancher-cluster-image-rke2-v1.32.4") || strings.Contains(fc.Cmd, ".raw") {
		t.Fatalf("framework_create cmd = %q", fc.Cmd)
	}
}

func TestBuildPlan_Advisor_AlwaysRunsAppFWFirst(t *testing.T) {
	p := InstallParams{Project: "appfw", PublicNet: "public", MgmtNet: "public",
		LBIP: "10.0.0.2", OSImage: "r.raw", FsImage: "m.qcow2", LBImage: "a.qcow2",
		Framework: "appfw", AdvisorFile: "cube-advisor-1.2.3.pigz", AdvisorLBIP: "10.0.0.9"}
	steps := BuildPlan(ModuleAdvisor, p, false, "/data", nil)
	var names []string
	for _, s := range steps {
		names = append(names, s.Name)
	}
	want := []string{"preflight", "import_fs", "import_lb", "import", "framework_create",
		"advisor_register", "install_advisor", "complete"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
}

func TestBuildPlan_Advisor_ChartVersionFromPigzName(t *testing.T) {
	p := InstallParams{Project: "appfw", OSImage: "r.raw", Framework: "appfw",
		AdvisorFile: "cube-advisor-1.2.3.pigz", AdvisorLBIP: "10.0.0.9"}
	steps := BuildPlan(ModuleAdvisor, p, false, "/data", nil)
	last := steps[len(steps)-2] // install_advisor
	if !strings.Contains(last.Cmd, " 1.2.3") {
		t.Fatalf("install cmd missing chart version: %q", last.Cmd)
	}
}

// The chart requires baseURL, and the origin a browser uses is not always the
// LoadBalancer's own address: behind TLS it is a hostname, through a tunnel it
// is localhost. A caller that says nothing still gets the old behaviour.
func TestAdvisorBaseURLDefaultsToTheLoadBalancer(t *testing.T) {
	if got := advisorBaseURL(InstallParams{AdvisorLBIP: "10.32.1.102"}); got != "http://10.32.1.102" {
		t.Errorf("advisorBaseURL = %q", got)
	}
}

func TestAdvisorBaseURLPrefersAnExplicitOrigin(t *testing.T) {
	p := InstallParams{AdvisorLBIP: "10.32.1.102", AdvisorBaseURL: "https://advisor.lab.example/"}
	if got := advisorBaseURL(p); got != "https://advisor.lab.example" {
		t.Errorf("advisorBaseURL = %q, want the explicit origin with no trailing slash", got)
	}
}

func TestAdvisorInstallPassesTheBaseURLToTheScript(t *testing.T) {
	// The script takes it as a fourth argument; a plan that drops it leaves the
	// chart's required value unset and helm aborts the install.
	steps := BuildPlan(ModuleAdvisor, InstallParams{
		Project: "appfw", Framework: "appfw", OSImage: "r.raw", FsImage: "m.qcow2",
		LBImage: "a.qcow2", AdvisorFile: "cube-advisor-1.2.3.pigz",
		AdvisorLBIP: "10.32.1.102", AdvisorBaseURL: "http://localhost:8082",
	}, false, "/data", nil)
	var found bool
	for _, s := range steps {
		if strings.Contains(s.Cmd, advisorInstallScriptName) {
			found = true
			if !strings.HasSuffix(s.Cmd, "http://localhost:8082") {
				t.Errorf("install step does not pass the base URL: %q", s.Cmd)
			}
		}
	}
	if !found {
		t.Fatal("no advisor install step in the plan")
	}
}
