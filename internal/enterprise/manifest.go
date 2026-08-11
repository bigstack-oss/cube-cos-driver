package enterprise

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Manifest describes the version-specific install settings for a CubeCOS
// release (which appctl to deploy, import args, air-gap support). Manifests
// live as JSON under <DataDir>/enterprise/manifests/.
type Manifest struct {
	Name  string `json:"name"`
	Match struct {
		Version string `json:"version"` // e.g. "3.1.0"
		Commit  string `json:"commit"`  // /etc/version commit; best-effort match
		Build   string `json:"build"`   // /etc/version build stamp, e.g. "20260313-1639"
	} `json:"match"`
	Appctl          string `json:"appctl"` // file under appfw/ to push to /usr/local/bin; "" = skip
	AirgapSupported bool   `json:"airgapSupported"`
	Import          struct {
		Tenant         string `json:"tenant"`
		Visibility     string `json:"visibility"`
		StorageBackend string `json:"storageBackend"`
		OS             string `json:"os"`
	} `json:"import"`
}

// importOrDefault fills unset import fields with the CubeCOS conventions.
func (m *Manifest) importDefaults() (tenant, visibility, storage, osName string) {
	tenant, visibility, storage, osName = "admin", "public", "CubeStorage", "Others"
	if m == nil {
		return
	}
	if m.Import.Tenant != "" {
		tenant = m.Import.Tenant
	}
	if m.Import.Visibility != "" {
		visibility = m.Import.Visibility
	}
	if m.Import.StorageBackend != "" {
		storage = m.Import.StorageBackend
	}
	if m.Import.OS != "" {
		osName = m.Import.OS
	}
	return
}

// LoadManifests reads every manifest JSON under <root>/manifests/. root is the
// configurable enterprise images folder.
func LoadManifests(root string) []Manifest {
	dir := filepath.Join(root, "manifests")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Manifest
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var m Manifest
		if json.Unmarshal(raw, &m) == nil && m.Name != "" {
			out = append(out, m)
		}
	}
	return out
}

// ParseVersion splits an /etc/version string (CUBE_<ver>_<build>_<commit>) into
// its version, build stamp, and commit.
func ParseVersion(etcVersion string) (version, build, commit string) {
	s := strings.TrimPrefix(strings.TrimSpace(etcVersion), "CUBE_")
	parts := strings.Split(s, "_")
	if len(parts) > 0 {
		version = parts[0]
	}
	if len(parts) > 1 {
		build = parts[1]
	}
	if len(parts) > 2 {
		commit = parts[2]
	}
	return
}

// MatchManifest picks the manifest for a cluster: an exact version+commit match
// wins; otherwise the same-version manifest with the closest build date.
func MatchManifest(ms []Manifest, version, build, commit string) *Manifest {
	var same []int
	for i := range ms {
		if ms[i].Match.Version != version {
			continue
		}
		if commit != "" && ms[i].Match.Commit == commit {
			return &ms[i]
		}
		same = append(same, i)
	}
	if len(same) == 0 {
		return nil
	}
	target := buildStamp(build)
	best := same[0]
	bestDiff := absDiff(target, buildStamp(ms[same[0]].Match.Build))
	for _, i := range same[1:] {
		if d := absDiff(target, buildStamp(ms[i].Match.Build)); d < bestDiff {
			bestDiff, best = d, i
		}
	}
	return &ms[best]
}

// buildStamp turns "20260313-1639" into a comparable integer.
func buildStamp(b string) int64 {
	n, _ := strconv.ParseInt(strings.ReplaceAll(b, "-", ""), 10, 64)
	return n
}

func absDiff(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

// ManifestNames returns the manifest names, for the version picker.
func ManifestNames(ms []Manifest) []string {
	out := make([]string, 0, len(ms))
	for i := range ms {
		out = append(out, ms[i].Name)
	}
	return out
}

// FindManifest returns the manifest with the given name.
func FindManifest(ms []Manifest, name string) *Manifest {
	for i := range ms {
		if ms[i].Name == name {
			return &ms[i]
		}
	}
	return nil
}
