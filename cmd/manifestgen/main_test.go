package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigstack-oss/cube-cos-driver/internal/enterprise"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGenerate(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "keys/p.pem"), "-----BEGIN PUBLIC KEY-----\nP\n-----END PUBLIC KEY-----\n")
	write(t, filepath.Join(d, "keys/m.pem"), "M\n")
	write(t, filepath.Join(d, "facts/cos-3.2.0.json"), `{"version":"3.2.0","airgapSupported":true,
	  "appfw":{"osImage":"r"},"advisorReleaseKey":{"p384":"p.pem","mldsa87":"m.pem"}}`)
	write(t, filepath.Join(d, "matrix.yaml"), `intermediate: ["3.1.10"]
releases:
  "3.2.0":
    cmp: [{ version: "2.1.1", status: supported }]
    advisor: [{ version: "0.4.25", status: untested }]
`)
	out, err := generate(filepath.Join(d, "matrix.yaml"), filepath.Join(d, "facts"), filepath.Join(d, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	var m enterprise.Manifest
	if err := json.Unmarshal(out["v3.2.0.json"], &m); err != nil {
		t.Fatal(err)
	}
	if m.Schema != 2 || m.Name != "v3.2.0" || m.Match.Version != "3.2.0" || *m.AirgapSupported != true {
		t.Fatalf("header: %+v", m)
	}
	if !strings.Contains(m.Trust.AdvisorReleaseKey.P384, "BEGIN PUBLIC KEY") || m.Trust.AdvisorReleaseKey.MLDSA87 != "M\n" {
		t.Fatalf("keys not inlined: %+v", m.Trust)
	}
	if m.Entry("cmp", "2.1.1") == nil || m.Entry("advisor", "0.4.25").Status != "untested" {
		t.Fatalf("modules: %+v", m.Modules)
	}
	if len(out) != 1 {
		t.Fatalf("intermediate releases must not get a manifest, got %d files", len(out))
	}
}

func TestGenerate_Rejects(t *testing.T) {
	for name, yml := range map[string]string{
		"bad status":          `releases: { "3.2.0": { cmp: [{ version: "2.1.1", status: great }] } }`,
		"build tag":           `releases: { "3.2.0": { cmp: [{ version: "2.1.1+rev1", status: supported }] } }`,
		"blocked no reason":   `releases: { "3.2.0": { cmp: [{ version: "2.1.1", status: blocked }] } }`,
		"no facts":            `releases: { "9.9.9": { cmp: [{ version: "2.1.1", status: supported }] } }`,
		"dup version":         `releases: { "3.2.0": { cmp: [{ version: "2.1.1", status: supported }, { version: "2.1.1", status: untested }] } }`,
		"intermediate listed": `{ intermediate: ["3.2.0"], releases: { "3.2.0": { cmp: [{ version: "2.1.1", status: supported }] } } }`,
	} {
		d := t.TempDir()
		write(t, filepath.Join(d, "facts/cos-3.2.0.json"), `{"version":"3.2.0"}`)
		write(t, filepath.Join(d, "matrix.yaml"), yml)
		if _, err := generate(filepath.Join(d, "matrix.yaml"), filepath.Join(d, "facts"), filepath.Join(d, "keys")); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
