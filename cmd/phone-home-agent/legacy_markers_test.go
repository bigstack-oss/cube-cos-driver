package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestParseImageVersion(t *testing.T) {
	cases := []struct {
		in      string
		want    version
		wantErr bool
	}{
		{in: "CUBE_3.1.0_20260313-1639_f58d033", want: version{3, 1, 0}},
		{in: "CUBE_3.1.10_20260807-1846_b459908", want: version{3, 1, 10}},
		{in: "3.1.10\n", want: version{3, 1, 10}},
		{in: "3.2", want: version{3, 2, 0}},
		{in: "", wantErr: true},
		{in: "not-a-version", wantErr: true},
	}
	for _, c := range cases {
		got, err := parseImageVersion(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseImageVersion(%q): expected error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseImageVersion(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseImageVersion(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// The whole point of the gate: rc6 (3.1.0) needs the workaround, 3.1.10 — which
// carries the hex fix — must be left alone.
func TestNeedsLegacyMarkerHandling(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"rc6 needs it", "CUBE_3.1.0_20260313-1639_f58d033", true},
		{"3.1.10 does not", "CUBE_3.1.10_20260807-1846_b459908", false},
		{"newer minor does not", "CUBE_3.2.0_x_y", false},
		{"older major needs it", "CUBE_3.0.0_x_y", true},
		{"unparseable is left alone", "garbage", false},
	}
	for _, c := range cases {
		p := filepath.Join(dir, "version-"+c.name)
		if err := os.WriteFile(p, []byte(c.content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := needsLegacyMarkerHandling(p); got != c.want {
			t.Errorf("%s: needsLegacyMarkerHandling(%q) = %v, want %v", c.name, c.content, got, c.want)
		}
	}
	if needsLegacyMarkerHandling(filepath.Join(dir, "absent")) {
		t.Error("missing version file should not trigger the legacy path")
	}
}

// makeSnapshot builds a snapshot-shaped zip for the strip/stamp round trip.
func makeSnapshot(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func zipNames(t *testing.T, path string) map[string]string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b := make([]byte, f.UncompressedSize64)
		_, _ = rc.Read(b)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestStripAndStampMarkers(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "snap.zip")
	makeSnapshot(t, src, map[string]string{
		"Comment":                             "Generated for 3cc-sky",
		"etc/policies/cubesys/cubesys1_0.yml": "role: control-converged\n",
		"etc/appliance/state/configured":      "",
		"etc/appliance/state/sla_accepted":    "",
	})

	dst := filepath.Join(dir, "snap-nomarkers.zip")
	removed, err := stripMarkers(src, dst)
	if err != nil {
		t.Fatalf("stripMarkers: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("expected 2 markers removed, got %d: %v", len(removed), removed)
	}

	got := zipNames(t, dst)
	for _, m := range markerFiles {
		if _, ok := got[m]; ok {
			t.Errorf("%s should have been stripped from the applied snapshot", m)
		}
	}
	// Everything else must survive untouched — this snapshot is what configures
	// the cluster, so dropping a policy would be far worse than the bug.
	if got["etc/policies/cubesys/cubesys1_0.yml"] != "role: control-converged\n" {
		t.Errorf("policy content not preserved: %q", got["etc/policies/cubesys/cubesys1_0.yml"])
	}
	if _, ok := got["Comment"]; !ok {
		t.Error("Comment entry missing from stripped snapshot")
	}

	root := filepath.Join(dir, "root")
	if err := stampMarkers(root, removed); err != nil {
		t.Fatalf("stampMarkers: %v", err)
	}
	for _, m := range markerFiles {
		if _, err := os.Stat(filepath.Join(root, m)); err != nil {
			t.Errorf("%s not stamped after apply: %v", m, err)
		}
	}
}

// A snapshot without markers must round-trip unchanged and report nothing
// removed, so the caller applies the original file.
func TestStripMarkersNoMarkers(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "snap.zip")
	makeSnapshot(t, src, map[string]string{"etc/policies/time/time1_0.yml": "timezone: UTC\n"})
	dst := filepath.Join(dir, "out.zip")
	removed, err := stripMarkers(src, dst)
	if err != nil {
		t.Fatalf("stripMarkers: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("expected no markers removed, got %v", removed)
	}
	if len(zipNames(t, dst)) != 1 {
		t.Error("non-marker entries should round-trip")
	}
}

// stampMarkers with nothing pending must be a no-op, since it runs on every
// successful apply including the modern (unstripped) path.
func TestStampMarkersNilIsNoop(t *testing.T) {
	if err := stampMarkers(t.TempDir(), nil); err != nil {
		t.Errorf("stampMarkers(nil): %v", err)
	}
}
