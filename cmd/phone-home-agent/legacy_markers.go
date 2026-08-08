package main

// Compatibility path for target images older than the hex fix that defers the
// snapshot's managed state markers.
//
// hex_config's MainApplySnapshot used to install the snapshot's managed
// state-marker files (etc/appliance/state/configured, sla_accepted) BEFORE
// running the module commits. On a fresh reimage that makes every module
// observe an already-configured node and skip first-time setup — pacemaker
// creates no VIP, keystone then cannot reach the DB, and the apply fails. The
// hex fix ("install snapshot managed markers only after a successful commit")
// landed after 3.1.0, so images at or below legacyMarkerMaxVersion need the
// same behaviour emulated from out here, without touching the target image.
//
// Emulation: strip the markers from a copy of the snapshot, apply that copy,
// then stamp the markers ourselves once the apply succeeds.

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// versionFile is the target image's version stamp, e.g.
// "CUBE_3.1.0_20260313-1639_f58d033".
const versionFile = "/etc/version"

// markerFiles are the snapshot-managed state markers hex installs. Paths are
// as stored in the snapshot zip (no leading slash).
var markerFiles = []string{
	"etc/appliance/state/configured",
	"etc/appliance/state/sla_accepted",
}

// legacyMarkerMaxVersion is the highest image version that still installs the
// markers before the commit. Images above it carry the hex fix and are applied
// untouched.
var legacyMarkerMaxVersion = version{3, 1, 0}

type version struct{ major, minor, patch int }

func (v version) String() string { return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch) }

// lessOrEqual reports whether v <= o.
func (v version) lessOrEqual(o version) bool {
	if v.major != o.major {
		return v.major < o.major
	}
	if v.minor != o.minor {
		return v.minor < o.minor
	}
	return v.patch <= o.patch
}

// parseImageVersion extracts the version from an /etc/version string such as
// "CUBE_3.1.0_20260313-1639_f58d033" (also tolerates a bare "3.1.10").
func parseImageVersion(s string) (version, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return version{}, fmt.Errorf("empty version string")
	}
	if i := strings.Index(s, "CUBE_"); i >= 0 {
		s = s[i+len("CUBE_"):]
	}
	if i := strings.IndexByte(s, '_'); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return version{}, fmt.Errorf("unrecognized version %q", s)
	}
	var v version
	var err error
	if v.major, err = strconv.Atoi(parts[0]); err != nil {
		return version{}, fmt.Errorf("unrecognized version %q", s)
	}
	if v.minor, err = strconv.Atoi(parts[1]); err != nil {
		return version{}, fmt.Errorf("unrecognized version %q", s)
	}
	if len(parts) > 2 {
		// tolerate trailing junk on the patch field
		digits := strings.TrimFunc(parts[2], func(r rune) bool { return r < '0' || r > '9' })
		if digits != "" {
			v.patch, _ = strconv.Atoi(digits)
		}
	}
	return v, nil
}

// needsLegacyMarkerHandling reports whether the image at versionFile predates
// the hex deferred-marker fix. Unknown/unreadable version → false: apply
// untouched rather than alter a snapshot for an image we cannot identify.
func needsLegacyMarkerHandling(versionFile string) bool {
	b, err := os.ReadFile(versionFile)
	if err != nil {
		log.Printf("legacy-markers: cannot read %s (%v) — applying snapshot unmodified", versionFile, err)
		return false
	}
	v, err := parseImageVersion(string(b))
	if err != nil {
		log.Printf("legacy-markers: %v — applying snapshot unmodified", err)
		return false
	}
	if v.lessOrEqual(legacyMarkerMaxVersion) {
		log.Printf("legacy-markers: image %s <= %s — stripping state markers for the apply",
			v, legacyMarkerMaxVersion)
		return true
	}
	return false
}

// stripMarkers copies src to dst omitting markerFiles, and returns the markers
// it removed (path → contents) so they can be stamped after a successful apply.
func stripMarkers(src, dst string) (map[string][]byte, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return nil, fmt.Errorf("open snapshot: %w", err)
	}
	defer zr.Close()

	out, err := os.Create(dst)
	if err != nil {
		return nil, fmt.Errorf("create stripped snapshot: %w", err)
	}
	defer out.Close()
	zw := zip.NewWriter(out)

	removed := map[string][]byte{}
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, "./")
		if isMarker(name) {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", f.Name, err)
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", f.Name, err)
			}
			removed[name] = b
			continue
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: f.Method, Modified: f.Modified})
		if err != nil {
			return nil, err
		}
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(w, rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return removed, nil
}

func isMarker(name string) bool {
	for _, m := range markerFiles {
		if name == m {
			return true
		}
	}
	return false
}

// stampMarkers writes the stripped markers into the live rootfs, matching the
// ownership/mode hex uses for them (root:www-data 0644 — group is left as-is
// here; hex re-applies ownership on its next commit).
func stampMarkers(root string, markers map[string][]byte) error {
	for name, content := range markers {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", p, err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", p, err)
		}
		log.Printf("legacy-markers: stamped %s after successful apply", p)
	}
	return nil
}
