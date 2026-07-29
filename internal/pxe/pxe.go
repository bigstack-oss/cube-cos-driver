// Package pxe manages a PXE server's GRUB boot config so the driver can offer
// an image dropdown at deploy time: enumerate the menu entries, temporarily
// repoint the default to an operator-picked image, and restore it after the
// nodes have booted. All writes to the shared grub.cfg are serialized by an
// advisory lock (the driver instances cooperate; see AcquireLock).
package pxe

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Entry is one GRUB menu entry (an installable PXE image).
type Entry struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

var (
	reMenu    = regexp.MustCompile(`(?m)^\s*menuentry\s+'([^']+)'`)
	reDefault = regexp.MustCompile(`(?m)^\s*set\s+default=(?:'([^']*)'|"([^"]*)"|(\S+))`)
)

// grubPath is the grub.cfg inside the PXE root.
func grubPath(root string) string { return filepath.Join(root, "grub.cfg") }

// currentDefault returns the value of `set default=` in the text ("" if none).
func currentDefault(text string) string {
	m := reDefault.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	for _, g := range m[1:] {
		if g != "" {
			return g
		}
	}
	return ""
}

// ListEntries parses the PXE root's grub.cfg into its menu entries, marking the
// one the `set default=` line currently points at. "Boot local disk" entries
// are excluded — they are not installable images.
func ListEntries(root string) ([]Entry, error) {
	b, err := os.ReadFile(grubPath(root))
	if err != nil {
		return nil, err
	}
	text := string(b)
	def := currentDefault(text)
	var out []Entry
	for _, m := range reMenu.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if strings.Contains(strings.ToLower(name), "local disk") {
			continue
		}
		out = append(out, Entry{Name: name, Default: name == def})
	}
	return out, nil
}

// CurrentDefault returns the grub.cfg's current default entry name.
func CurrentDefault(root string) (string, error) {
	b, err := os.ReadFile(grubPath(root))
	if err != nil {
		return "", err
	}
	return currentDefault(string(b)), nil
}

// SetDefault rewrites the `set default=` line to point at entry. It errors if
// entry is not a real menuentry (so we never point the default at nothing).
func SetDefault(root, entry string) error {
	path := grubPath(root)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(b)
	found := false
	for _, m := range reMenu.FindAllStringSubmatch(text, -1) {
		if m[1] == entry {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("no menuentry %q in %s", entry, path)
	}
	if !reDefault.MatchString(text) {
		return fmt.Errorf("no 'set default=' line in %s", path)
	}
	out := reDefault.ReplaceAllString(text, "set default='"+entry+"'")
	return writeFileAtomic(path, []byte(out), 0o644)
}

// writeFileAtomic writes via a temp file + rename in the same dir so a reader
// (or a crash) never sees a half-written grub.cfg.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".grub.cfg.*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Chmod(tmp, perm)
	return os.Rename(tmp, path)
}

// scanFirstLine returns the first line of a reader (holder identity in a lock).
func scanFirstLine(r *os.File) string {
	s := bufio.NewScanner(r)
	if s.Scan() {
		return strings.TrimSpace(s.Text())
	}
	return ""
}

// lockName is the advisory lockfile in the PXE root guarding the default entry.
const lockName = ".cube-default.lock"

// lockTTL bounds how long a lock is honored before another driver may steal it
// — so a crashed/hung holder (e.g. a node stuck in POST) can't wedge the shared
// default forever.
const lockTTL = 30 * time.Minute

// AcquireLock takes the advisory default-entry lock in the PXE root. holder
// identifies this driver (host:pid) and is recorded for diagnostics. It is a
// cooperative lock: only cube-cos-driver instances honor it. Returns a release
// func. now is injected for tests.
func AcquireLock(root, holder string, now func() time.Time) (release func(), err error) {
	path := filepath.Join(root, lockName)
	acquire := func() (*os.File, error) {
		return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	}
	f, err := acquire()
	if err != nil {
		if !os.IsExist(err) {
			return nil, err
		}
		// Held — steal only if stale (older than TTL).
		if fi, serr := os.Stat(path); serr == nil && now().Sub(fi.ModTime()) > lockTTL {
			_ = os.Remove(path)
			if f, err = acquire(); err != nil {
				return nil, fmt.Errorf("lock held and stale-steal failed: %w", err)
			}
		} else {
			existing := ""
			if ef, oerr := os.Open(path); oerr == nil {
				existing = scanFirstLine(ef)
				ef.Close()
			}
			return nil, fmt.Errorf("PXE default is busy (held by %s) — another deploy is booting", existing)
		}
	}
	fmt.Fprintf(f, "%s\t%s\n", holder, now().UTC().Format(time.RFC3339))
	f.Close()
	return func() { os.Remove(path) }, nil
}
