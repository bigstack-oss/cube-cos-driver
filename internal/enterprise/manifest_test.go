package enterprise

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A manifest that omits airgapSupported must not claim the image cannot simulate
// an air-gap: unset means "let the cluster answer", since ClusterInfo probes for
// hex_sdk airgap_sim_apply. Only an explicit value overrides that probe.
func TestManifest_AirgapSupportedUnsetIsNotFalse(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "manifests") // LoadManifests reads <root>/manifests
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("unset.json", `{"name":"unset","match":{"version":"3.1.20"}}`)
	write("off.json", `{"name":"off","match":{"version":"3.1.0"},"airgapSupported":false}`)
	write("on.json", `{"name":"on","match":{"version":"3.2.0"},"airgapSupported":true}`)

	loaded := LoadManifests(root)
	if len(loaded) != 3 {
		t.Fatalf("loaded %d manifests, want 3 — the fixture is not being read", len(loaded))
	}
	got := map[string]*bool{}
	for _, m := range loaded {
		got[m.Name] = m.AirgapSupported
	}
	if got["unset"] != nil {
		t.Fatalf("unset manifest: AirgapSupported = %v, want nil so the cluster probe decides", *got["unset"])
	}
	if got["off"] == nil || *got["off"] {
		t.Fatalf("explicit false must stay false, got %v", got["off"])
	}
	if got["on"] == nil || !*got["on"] {
		t.Fatalf("explicit true must stay true, got %v", got["on"])
	}
}

func TestManifest_AppfwOSImage(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "manifests")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "v.json"), []byte(`{"name":"v3.2.0","match":{"version":"3.2.0"},"appfw":{"osImage":"rancher-cluster-image-rke2-v1.32.4"}}`), 0o644)
	m := FindManifest(LoadManifests(root), "v3.2.0")
	if m == nil || m.Appfw == nil || m.Appfw.OSImage != "rancher-cluster-image-rke2-v1.32.4" {
		t.Fatalf("appfw.osImage not decoded: %+v", m)
	}
}

func TestManifest_V2RoundTrip(t *testing.T) {
	raw := `{"schema":2,"name":"v3.2.0","match":{"version":"3.2.0"},
	 "appfw":{"extpack":{"file":"CUBE_3.2.0_x_.ext"},"osImage":"rancher-cluster-image-rke2-v1.32.4"},
	 "trust":{"advisorReleaseKey":{"p384":"-----BEGIN PUBLIC KEY-----\nX\n-----END PUBLIC KEY-----\n","mldsa87":"Y"}},
	 "modules":{"cmp":[{"version":"2.1.1","status":"supported"}],
	  "advisor":[{"version":"0.4.25","status":"untested"}]}}`
	var m Manifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.Schema != 2 || m.Appfw.Extpack.File != "CUBE_3.2.0_x_.ext" || m.Appfw.OSImage == "" {
		t.Fatalf("appfw not decoded: %+v", m.Appfw)
	}
	if m.Trust.AdvisorReleaseKey.MLDSA87 != "Y" {
		t.Fatalf("trust not decoded: %+v", m.Trust)
	}
	if got := m.Modules["cmp"][0]; got.Version != "2.1.1" || got.Status != "supported" {
		t.Fatalf("cmp entry: %+v", got)
	}
}

func TestManifest_V1HasNoModules(t *testing.T) {
	var m Manifest
	if err := json.Unmarshal([]byte(`{"name":"v3.1.20","match":{"version":"3.1.20"}}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Schema != 0 || m.Modules != nil || m.Trust != nil {
		t.Fatalf("v1 manifest grew v2 fields: %+v", m)
	}
}
