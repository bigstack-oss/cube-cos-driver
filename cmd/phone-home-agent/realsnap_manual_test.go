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
