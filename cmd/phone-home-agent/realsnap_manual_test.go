package main

import (
	"os"
	"testing"
)

// Guarded manual check against a real snapshot pulled off a node:
//
//	REAL_SNAPSHOT=/tmp/real.snapshot go test ./cmd/phone-home-agent -run RealSnapshot -v
func TestRealSnapshotStrip(t *testing.T) {
	src := os.Getenv("REAL_SNAPSHOT")
	if src == "" {
		t.Skip("REAL_SNAPSHOT not set")
	}
	dst := t.TempDir() + "/stripped.zip"
	removed, err := stripMarkers(src, dst)
	if err != nil {
		t.Fatalf("stripMarkers on real snapshot: %v", err)
	}
	t.Logf("removed %d markers: %v", len(removed), keys(removed))
	for name := range zipNames(t, dst) {
		if isMarker(name) {
			t.Errorf("marker %s survived the strip", name)
		}
		t.Logf("kept: %s", name)
	}
}

func keys(m map[string][]byte) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Same guarded manual check for the timezone guard: read the zone out of a
// real snapshot's time policy and report what the guard would decide.
func TestRealSnapshotTimezone(t *testing.T) {
	src := os.Getenv("REAL_SNAPSHOT")
	if src == "" {
		t.Skip("REAL_SNAPSHOT not set")
	}
	tz, err := snapshotTimezone(src)
	if err != nil {
		t.Fatalf("snapshotTimezone on real snapshot: %v", err)
	}
	if tz == "" {
		t.Fatal("no timezone found in the real snapshot's time policy")
	}
	t.Logf("real snapshot timezone: %q", tz)
	if verFile := os.Getenv("REAL_VERSION_FILE"); verFile != "" {
		t.Logf("guard verdict against %s: %v", verFile, checkLegacyTimezone(verFile, src))
	}
}
