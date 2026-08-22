package enterprise

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverArtifacts_BasicDiscovery(t *testing.T) {
	tmp := t.TempDir()

	// Create enterprise/appfw/ with files
	appfwDir := filepath.Join(tmp, "enterprise", "appfw")
	if err := os.MkdirAll(appfwDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create enterprise/cubecmp/ with files
	cmpDir := filepath.Join(tmp, "enterprise", "cubecmp")
	if err := os.MkdirAll(cmpDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create enterprise/advisor/ with files
	advisorDir := filepath.Join(tmp, "enterprise", "advisor")
	if err := os.MkdirAll(advisorDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create test files in appfw
	if err := os.WriteFile(filepath.Join(appfwDir, "rancher-cluster-image-rke2-v1.32.4.raw"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appfwDir, "amphora-x64-haproxy-yoga.qcow2"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Create test files in cubecmp
	if err := os.WriteFile(filepath.Join(cmpDir, "cube-portal-2.1.0.pigz"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Create test file in advisor
	if err := os.WriteFile(filepath.Join(advisorDir, "cube-advisor-1.2.3.pigz"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	got := DiscoverArtifacts(filepath.Join(tmp, "enterprise"))

	// Verify AppFW names are sorted
	if len(got.AppFW) != 2 {
		t.Fatalf("got %d AppFW files, want 2", len(got.AppFW))
	}
	if got.AppFW[0] != "amphora-x64-haproxy-yoga.qcow2" || got.AppFW[1] != "rancher-cluster-image-rke2-v1.32.4.raw" {
		t.Fatalf("AppFW = %v, want [amphora-x64-haproxy-yoga.qcow2 rancher-cluster-image-rke2-v1.32.4.raw]", got.AppFW)
	}

	// Verify CMP names
	if len(got.CMP) != 1 {
		t.Fatalf("got %d CMP files, want 1", len(got.CMP))
	}
	if got.CMP[0] != "cube-portal-2.1.0.pigz" {
		t.Fatalf("CMP = %v, want [cube-portal-2.1.0.pigz]", got.CMP)
	}

	// Verify Advisor names
	if len(got.Advisor) != 1 {
		t.Fatalf("got %d Advisor files, want 1", len(got.Advisor))
	}
	if got.Advisor[0] != "cube-advisor-1.2.3.pigz" {
		t.Fatalf("Advisor = %v, want [cube-advisor-1.2.3.pigz]", got.Advisor)
	}
}

func TestDiscoverArtifacts_NoEnterpriseDir(t *testing.T) {
	tmp := t.TempDir()
	// Don't create any enterprise dir
	got := DiscoverArtifacts(filepath.Join(tmp, "enterprise"))

	// Should return empty slices, not nil and not panic
	if got.AppFW == nil {
		t.Fatal("AppFW should be empty slice, not nil")
	}
	if got.CMP == nil {
		t.Fatal("CMP should be empty slice, not nil")
	}
	if len(got.AppFW) != 0 || len(got.CMP) != 0 {
		t.Fatalf("expected empty slices, got AppFW=%v CMP=%v", got.AppFW, got.CMP)
	}
}

func TestDiscoverArtifacts_SkipDotfilesAndDirs(t *testing.T) {
	tmp := t.TempDir()

	appfwDir := filepath.Join(tmp, "enterprise", "appfw")
	if err := os.MkdirAll(appfwDir, 0755); err != nil {
		t.Fatal(err)
	}

	cmpDir := filepath.Join(tmp, "enterprise", "cubecmp")
	if err := os.MkdirAll(cmpDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a valid file
	if err := os.WriteFile(filepath.Join(appfwDir, "valid-artifact.raw"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a dotfile - should be skipped
	if err := os.WriteFile(filepath.Join(appfwDir, ".hidden-artifact.raw"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a subdirectory - should be skipped
	if err := os.MkdirAll(filepath.Join(appfwDir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create a file in the subdirectory - should be skipped
	if err := os.WriteFile(filepath.Join(appfwDir, "subdir", "nested-artifact.raw"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a valid file in cubecmp
	if err := os.WriteFile(filepath.Join(cmpDir, "another-artifact.pigz"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	got := DiscoverArtifacts(filepath.Join(tmp, "enterprise"))

	// Only valid files at top level should be returned
	if len(got.AppFW) != 1 || got.AppFW[0] != "valid-artifact.raw" {
		t.Fatalf("AppFW = %v, want [valid-artifact.raw]", got.AppFW)
	}

	if len(got.CMP) != 1 || got.CMP[0] != "another-artifact.pigz" {
		t.Fatalf("CMP = %v, want [another-artifact.pigz]", got.CMP)
	}
}

func TestDiscoverArtifacts_EmptyDirs(t *testing.T) {
	tmp := t.TempDir()

	// Create empty enterprise dirs
	appfwDir := filepath.Join(tmp, "enterprise", "appfw")
	if err := os.MkdirAll(appfwDir, 0755); err != nil {
		t.Fatal(err)
	}

	cmpDir := filepath.Join(tmp, "enterprise", "cubecmp")
	if err := os.MkdirAll(cmpDir, 0755); err != nil {
		t.Fatal(err)
	}

	got := DiscoverArtifacts(filepath.Join(tmp, "enterprise"))

	// Empty dirs should return empty slices
	if len(got.AppFW) != 0 || len(got.CMP) != 0 {
		t.Fatalf("expected empty slices, got AppFW=%v CMP=%v", got.AppFW, got.CMP)
	}
}
