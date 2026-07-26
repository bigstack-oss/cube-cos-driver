package generator

import (
	"archive/zip"
	"bytes"
	"io"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/bigstack-oss/cube-cos-driver/internal/model"
)

func readZip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestBuildNodeSnapshot(t *testing.T) {
	var d model.ClusterDetail
	mustUnmarshalFile(t, "testdata/fixtures/ha3.json", &d)
	ctl := GetControlInfo(d.NodeData)
	now := time.Date(2026, 7, 24, 12, 34, 56, 0, time.UTC)

	data, err := BuildNodeSnapshot(d.NodeData[1], d, ctl, now)
	if err != nil {
		t.Fatal(err)
	}
	files := readZip(t, data)

	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	want := []string{
		"Comment",
		"etc/appliance/state/configured",
		"etc/appliance/state/sla_accepted",
		"etc/policies/cubesys/cubesys1_0.yml",
		"etc/policies/network/network1_0.yml",
		"etc/policies/time/time1_0.yml",
	}
	if len(names) != len(want) {
		t.Fatalf("entries = %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("entries = %v, want %v", names, want)
		}
	}

	comment := files["Comment"]
	re := regexp.MustCompile(`^Generated for sky-lab on \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\n$`)
	if !re.MatchString(comment) {
		t.Fatalf("Comment = %q", comment)
	}
	if comment != "Generated for sky-lab on 2026-07-24 12:34:56\n" {
		t.Fatalf("Comment timestamp = %q", comment)
	}

	if files["etc/policies/cubesys/cubesys1_0.yml"] != RenderCubesys(d.NodeData[1], d.ClusterConfig, ctl) {
		t.Fatal("cubesys content mismatch")
	}
	if files["etc/policies/network/network1_0.yml"] != RenderNetwork(d.NodeData[1], d.ClusterConfig) {
		t.Fatal("network content mismatch")
	}
}

func TestBuildClusterZip(t *testing.T) {
	var d model.ClusterDetail
	mustUnmarshalFile(t, "testdata/fixtures/ha3.json", &d)
	ctl := GetControlInfo(d.NodeData)
	now := time.Now()

	snaps := map[string][]byte{}
	for _, n := range d.NodeData {
		b, err := BuildNodeSnapshot(n, d, ctl, now)
		if err != nil {
			t.Fatal(err)
		}
		snaps[n.Hostname] = b
	}
	detailJSON := []byte(`{"stub": true}`)
	data, err := BuildClusterZip(d.ShortID(), detailJSON, snaps)
	if err != nil {
		t.Fatal(err)
	}
	files := readZip(t, data)
	if string(files[d.ShortID()+"/clusterDetail.json"]) != string(detailJSON) {
		t.Fatal("clusterDetail.json missing or wrong")
	}
	for _, n := range d.NodeData {
		if _, ok := files[d.ShortID()+"/"+n.Hostname+".snapshot"]; !ok {
			t.Fatalf("missing %s.snapshot", n.Hostname)
		}
	}
	if len(files) != 4 {
		t.Fatalf("unexpected extra entries: %d", len(files))
	}
}
