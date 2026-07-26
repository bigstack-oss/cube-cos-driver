package storage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bigstack-oss/cube-cos-driver/internal/model"
)

func loadHA3(t *testing.T) model.ClusterDetail {
	t.Helper()
	raw, err := os.ReadFile("../model/testdata/ha3.json")
	if err != nil {
		t.Fatal(err)
	}
	var d model.ClusterDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func newStore(t *testing.T, withExport bool) *Store {
	t.Helper()
	s := &Store{DataDir: t.TempDir()}
	if withExport {
		s.ExportDir = t.TempDir()
	}
	return s
}

func TestSaveListDetail(t *testing.T) {
	s := newStore(t, false)
	d := loadHA3(t)
	if err := s.Save(d); err != nil {
		t.Fatal(err)
	}

	digests, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(digests) != 1 || digests[0].ID != d.ShortID() || digests[0].Name != "sky-lab" {
		t.Fatalf("digests = %+v", digests)
	}
	if len(digests[0].Nodes) != 3 || digests[0].Nodes[0] != "cube-1" {
		t.Fatalf("nodes = %v", digests[0].Nodes)
	}

	got, err := s.Detail(d.ShortID())
	if err != nil {
		t.Fatal(err)
	}
	if got.ClusterInfo.Name != d.ClusterInfo.Name || len(got.NodeData) != 3 {
		t.Fatal("detail round trip mismatch")
	}

	zipPath, name, err := s.ClusterZipPath(d.ShortID())
	if err != nil {
		t.Fatal(err)
	}
	if name != "sky-lab.zip" {
		t.Fatalf("download name = %q", name)
	}
	if _, err := os.Stat(zipPath); err != nil {
		t.Fatal(err)
	}

	snapPath, err := s.SnapshotPath(d.ShortID(), "cube-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapPath); err != nil {
		t.Fatal(err)
	}
}

func TestExportDirLifecycle(t *testing.T) {
	s := newStore(t, true)
	d := loadHA3(t)
	if err := s.Save(d); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"cube-1", "cube-2", "cube-3"} {
		if _, err := os.Stat(filepath.Join(s.ExportDir, h+".snapshot")); err != nil {
			t.Fatalf("export missing: %v", err)
		}
	}

	// Rename a node and re-save: stale export must be replaced.
	d2 := loadHA3(t)
	d2.NodeData[2].Hostname = "cube-9"
	if err := s.Save(d2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.ExportDir, "cube-3.snapshot")); !os.IsNotExist(err) {
		t.Fatal("stale export cube-3.snapshot still present")
	}
	if _, err := os.Stat(filepath.Join(s.ExportDir, "cube-9.snapshot")); err != nil {
		t.Fatal("new export cube-9.snapshot missing")
	}

	if err := s.Delete(d.ShortID()); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(s.ExportDir)
	if len(entries) != 0 {
		t.Fatalf("export dir not emptied: %v", entries)
	}
	entries, _ = os.ReadDir(s.DataDir)
	if len(entries) != 0 {
		t.Fatalf("data dir not emptied: %v", entries)
	}
}

func TestNotFoundAndInvalid(t *testing.T) {
	s := newStore(t, false)
	if _, err := s.Detail("nope00000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := s.Delete("nope00000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	d := loadHA3(t)
	d.NodeData[0].Hostname = "" // invalid
	if err := s.Save(d); err == nil {
		t.Fatal("expected validation error")
	}
	entries, _ := os.ReadDir(s.DataDir)
	if len(entries) != 0 {
		t.Fatalf("failed save left artifacts: %v", entries)
	}
}

func TestSaveOverwriteKeepsConsistency(t *testing.T) {
	s := newStore(t, false)
	d := loadHA3(t)
	if err := s.Save(d); err != nil {
		t.Fatal(err)
	}
	d.ClusterInfo.Name = "sky-lab-2"
	if err := s.Save(d); err != nil {
		t.Fatal(err)
	}
	got, err := s.Detail(d.ShortID())
	if err != nil {
		t.Fatal(err)
	}
	if got.ClusterInfo.Name != "sky-lab-2" {
		t.Fatal("overwrite lost update")
	}
	digests, _ := s.List()
	if len(digests) != 1 {
		t.Fatalf("digests = %+v", digests)
	}
}
