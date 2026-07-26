package generator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigstack-oss/cube-cos-driver/internal/model"
)

func diff(want, got string) string {
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")
	var b strings.Builder
	n := max(len(w), len(g))
	for i := 0; i < n; i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			fmt.Fprintf(&b, "line %d:\n  want: %q\n  got:  %q\n", i+1, wl, gl)
		}
	}
	return b.String()
}

func mustUnmarshalFile(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

func TestRenderGolden(t *testing.T) {
	fixtures, err := filepath.Glob("testdata/fixtures/*.json")
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, f := range fixtures {
		var d model.ClusterDetail
		mustUnmarshalFile(t, f, &d)
		ctl := GetControlInfo(d.NodeData)
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		for _, n := range d.NodeData {
			dir := filepath.Join("testdata/golden", name, n.Hostname)
			check := func(file, got string) {
				t.Helper()
				want, err := os.ReadFile(filepath.Join(dir, file))
				if err != nil {
					t.Fatal(err)
				}
				if got != string(want) {
					t.Errorf("%s/%s/%s mismatch:\n%s", name, n.Hostname, file, diff(string(want), got))
				}
			}
			check("cubesys1_0.yml", RenderCubesys(n, d.ClusterConfig, ctl))
			check("network1_0.yml", RenderNetwork(n, d.ClusterConfig))
			check("time1_0.yml", RenderTime(d.ClusterConfig))
		}
	}
}

func TestGetControlInfo(t *testing.T) {
	var d model.ClusterDetail
	mustUnmarshalFile(t, "testdata/fixtures/mixed-roles.json", &d)
	ctl := GetControlInfo(d.NodeData)
	// control, moderator, edge-core have control function; compute/storage don't.
	if strings.Join(ctl.Hostnames, ",") != "ctrl-1,mod-1,edge-1" {
		t.Fatalf("hostnames = %v", ctl.Hostnames)
	}
	if ctl.IPs[0] != "10.10.0.1" {
		t.Fatalf("ips = %v", ctl.IPs)
	}
}
