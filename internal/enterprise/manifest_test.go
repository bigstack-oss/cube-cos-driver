package enterprise

import (
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
