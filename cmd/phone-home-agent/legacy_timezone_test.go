package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTimezoneValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{"---\nname: time\nversion: 1.0\ntimezone: Asia/Taipei\n", "Asia/Taipei"},
		{"timezone: UTC\n", "UTC"},
		{"timezone:    UTC   \n", "UTC"},
		{"timezone: \"Asia/Taipei\"\n", "Asia/Taipei"},
		{"timezone: 'UTC'\n", "UTC"},
		{"# timezone: UTC\nname: time\n", ""},
		{"name: time\n", ""},
	}
	for _, c := range cases {
		if got := parseTimezoneValue(c.in); got != c.want {
			t.Errorf("parseTimezoneValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSnapshotTimezone(t *testing.T) {
	dir := t.TempDir()

	withTZ := filepath.Join(dir, "tz.zip")
	makeSnapshot(t, withTZ, map[string]string{
		"etc/policies/time/time1_0.yml":       "---\ntimezone: Asia/Taipei\n",
		"etc/policies/cubesys/cubesys1_0.yml": "role: control-converged\n",
	})
	got, err := snapshotTimezone(withTZ)
	if err != nil {
		t.Fatalf("snapshotTimezone: %v", err)
	}
	if got != "Asia/Taipei" {
		t.Errorf("timezone = %q, want Asia/Taipei", got)
	}

	// No time policy at all must be "" with no error — the caller allows it.
	noTZ := filepath.Join(dir, "notz.zip")
	makeSnapshot(t, noTZ, map[string]string{"etc/policies/cubesys/cubesys1_0.yml": "role: undef\n"})
	got, err = snapshotTimezone(noTZ)
	if err != nil || got != "" {
		t.Errorf("absent time policy: got (%q, %v), want (\"\", nil)", got, err)
	}

	if _, err := snapshotTimezone(filepath.Join(dir, "absent.zip")); err == nil {
		t.Error("a missing snapshot should error")
	}
}

// The guard's whole point: block UTC on rc6, allow it on the fixed image, and
// never block on something it could not determine.
func TestCheckLegacyTimezone(t *testing.T) {
	dir := t.TempDir()

	snap := func(name, tz string) string {
		p := filepath.Join(dir, name)
		entries := map[string]string{"etc/policies/cubesys/cubesys1_0.yml": "role: control-converged\n"}
		if tz != "" {
			entries["etc/policies/time/time1_0.yml"] = "---\ntimezone: " + tz + "\n"
		}
		makeSnapshot(t, p, entries)
		return p
	}
	verFile := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	rc6 := verFile("v-rc6", "CUBE_3.1.0_20260313-1639_f58d033")
	fixed := verFile("v-fixed", "CUBE_3.1.10_20260807-1846_b459908")
	junk := verFile("v-junk", "garbage")

	cases := []struct {
		name      string
		version   string
		snapshot  string
		wantBlock bool
	}{
		{"rc6 + UTC is blocked", rc6, snap("utc.zip", "UTC"), true},
		{"rc6 + Asia/Taipei is fine", rc6, snap("tpe.zip", "Asia/Taipei"), false},
		{"rc6 + America/New_York is fine", rc6, snap("nyc.zip", "America/New_York"), false},
		{"fixed image + UTC is fine", fixed, snap("utc2.zip", "UTC"), false},
		{"unparseable version is not blocked", junk, snap("utc3.zip", "UTC"), false},
		{"missing version file is not blocked", filepath.Join(dir, "nope"), snap("utc4.zip", "UTC"), false},
		{"no time policy is not blocked", rc6, snap("none.zip", ""), false},
		{"unreadable snapshot is not blocked", rc6, filepath.Join(dir, "absent.zip"), false},
	}
	for _, c := range cases {
		err := checkLegacyTimezone(c.version, c.snapshot)
		if c.wantBlock && err == nil {
			t.Errorf("%s: expected the apply to be refused", c.name)
		}
		if !c.wantBlock && err != nil {
			t.Errorf("%s: unexpected refusal: %v", c.name, err)
		}
	}
}

// The refusal has to tell an operator what to change, not just that it failed.
func TestCheckLegacyTimezoneMessage(t *testing.T) {
	dir := t.TempDir()
	v := filepath.Join(dir, "version")
	if err := os.WriteFile(v, []byte("CUBE_3.1.0_20260313-1639_f58d033"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := filepath.Join(dir, "snap.zip")
	makeSnapshot(t, s, map[string]string{"etc/policies/time/time1_0.yml": "timezone: UTC\n"})

	err := checkLegacyTimezone(v, s)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"UTC", "3.1.0", "Asia/Taipei"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal should mention %q: %v", want, err)
		}
	}
}
