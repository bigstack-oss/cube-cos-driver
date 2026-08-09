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
