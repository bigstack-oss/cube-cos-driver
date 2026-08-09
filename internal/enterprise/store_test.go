package enterprise

import (
	"reflect"
	"testing"
)

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	in := &Install{
		ClusterID: "cluster-1",
		Module:    ModuleAppFW,
		StartedAt: "2026-08-08T00:00:00Z",
		Params:    InstallParams{Project: "proj"},
		Steps: []*Step{
			{Name: "preflight", Title: "Preflight checks", State: StepDone},
			{Name: "import", Title: "Import Rancher cluster image", State: StepActive},
		},
		Current: 1,
		State:   "running",
		Portal:  "https://example.test",
	}

	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, ok := s.Load(in.ClusterID, in.Module)
	if !ok {
		t.Fatalf("Load: expected ok=true")
	}
	if got.ClusterID != in.ClusterID {
		t.Errorf("ClusterID = %q, want %q", got.ClusterID, in.ClusterID)
	}
	if got.Module != in.Module {
		t.Errorf("Module = %q, want %q", got.Module, in.Module)
	}
	if got.State != in.State {
		t.Errorf("State = %q, want %q", got.State, in.State)
	}
	if !reflect.DeepEqual(got.Steps, in.Steps) {
		t.Errorf("Steps = %+v, want %+v", got.Steps, in.Steps)
	}
}

func TestStoreLoadMissing(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	got, ok := s.Load("no-such-cluster", ModuleCMP)
	if ok || got != nil {
		t.Errorf("Load(missing) = (%v, %v), want (nil, false)", got, ok)
	}
}
