package main

// Guard for target images older than the horizon timezone-escape fix.
//
// Pre-fix config_horizon builds the django timezone escape by splitting the
// zone on '/' and rejoining tzv[0] + "\/" + tzv[1]. A single-component zone
// such as "UTC" leaves tzv with one element, so tzv[1] reads past the end of
// the vector — undefined behaviour that segfaults hex_config partway through
// the commit and rolls the entire apply back. Being UB it is not consistent:
// on a 3-node install it took out two nodes and spared the third.
//
// Fixed upstream by cubecos 20fd064; images at or below
// legacyTimezoneMaxVersion still carry it. Rewriting the operator's timezone
// here would silently change cluster-wide behaviour, so the agent refuses the
// apply instead and names the cause — better than a segfault minutes in.

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

// legacyTimezoneMaxVersion is the highest image version whose horizon module
// crashes on a single-component timezone.
var legacyTimezoneMaxVersion = version{3, 1, 0}

// timePolicyDir is where the time policy lives inside a snapshot zip.
const timePolicyDir = "etc/policies/time/"

// checkLegacyTimezone returns an error when the target image predates the
// horizon fix and the snapshot's timezone has no '/'. Anything it cannot
// determine — unreadable version, absent or unparseable time policy — is
// allowed through, matching the legacy-marker gate: never block on a guess.
func checkLegacyTimezone(versionFile, snapshot string) error {
	b, err := os.ReadFile(versionFile)
	if err != nil {
		return nil
	}
	v, err := parseImageVersion(string(b))
	if err != nil {
		return nil
	}
	if !v.lessOrEqual(legacyTimezoneMaxVersion) {
		return nil
	}
	tz, err := snapshotTimezone(snapshot)
	if err != nil {
		log.Printf("legacy-timezone: cannot read the snapshot's time policy (%v) — not blocking the apply", err)
		return nil
	}
	if tz == "" {
		return nil
	}
	if strings.Contains(tz, "/") {
		log.Printf("legacy-timezone: image %s with timezone %q — ok, two components", v, tz)
		return nil
	}
	return fmt.Errorf("timezone %q is unsafe on image %s: its horizon module splits the zone on '/' "+
		"and reads past the end of the vector for a single-component zone, segfaulting hex_config "+
		"mid-commit. Use a two-component zone such as Asia/Taipei, or an image above %s (cubecos 20fd064)",
		tz, v, legacyTimezoneMaxVersion)
}

// snapshotTimezone reads the `timezone:` value from the snapshot's time policy.
// An absent policy is not an error — it yields "", which the caller allows.
func snapshotTimezone(snapshot string) (string, error) {
	zr, err := zip.OpenReader(snapshot)
	if err != nil {
		return "", fmt.Errorf("open snapshot: %w", err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, "./")
		if !strings.HasPrefix(name, timePolicyDir) || !strings.HasSuffix(name, ".yml") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("read %s: %w", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return "", fmt.Errorf("read %s: %w", f.Name, err)
		}
		if tz := parseTimezoneValue(string(b)); tz != "" {
			return tz, nil
		}
	}
	return "", nil
}

// parseTimezoneValue pulls the value of the top-level `timezone:` key.
func parseTimezoneValue(policy string) string {
	for _, line := range strings.Split(policy, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		rest, ok := strings.CutPrefix(line, "timezone:")
		if !ok {
			continue
		}
		return strings.Trim(strings.TrimSpace(rest), `"'`)
	}
	return ""
}
