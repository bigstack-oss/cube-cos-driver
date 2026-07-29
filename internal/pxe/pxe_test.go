package pxe

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sampleGrub = `set default='travis_cubecos (UEFI)'
set timeout=5

menuentry 'Boot local disk (UEFI)' {
  chainloader /grub2/shimx64.efi
}
menuentry  'travis_cubecos (UEFI)' { linuxefi /travis_cubecos/bzImage ; initrdefi /travis_cubecos/pxe_initramfs.cgz }
menuentry  'v3.1.0-rc6 (UEFI)' { linuxefi /v3.1.0-rc6/vmlinuz ; initrdefi /v3.1.0-rc6/initrd.img }
`

func writeGrub(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "grub.cfg"), []byte(sampleGrub), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestListEntriesExcludesLocalDisk(t *testing.T) {
	got, err := ListEntries(writeGrub(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("entries = %+v, want 2 (local disk excluded)", got)
	}
	byName := map[string]bool{}
	for _, e := range got {
		byName[e.Name] = e.Default
	}
	if !byName["travis_cubecos (UEFI)"] {
		t.Error("travis_cubecos should be marked default")
	}
	if byName["v3.1.0-rc6 (UEFI)"] {
		t.Error("v3.1.0-rc6 should not be default")
	}
}

func TestSetDefaultFlipsAndRestores(t *testing.T) {
	root := writeGrub(t)
	if err := SetDefault(root, "v3.1.0-rc6 (UEFI)"); err != nil {
		t.Fatal(err)
	}
	if d, _ := CurrentDefault(root); d != "v3.1.0-rc6 (UEFI)" {
		t.Fatalf("default = %q after flip", d)
	}
	// menuentries and timeout untouched
	if got, _ := ListEntries(root); len(got) != 2 {
		t.Fatalf("entries changed after flip: %+v", got)
	}
	if err := SetDefault(root, "travis_cubecos (UEFI)"); err != nil {
		t.Fatal(err)
	}
	if d, _ := CurrentDefault(root); d != "travis_cubecos (UEFI)" {
		t.Fatalf("restore failed, default = %q", d)
	}
}

func TestSetDefaultRejectsUnknownEntry(t *testing.T) {
	if err := SetDefault(writeGrub(t), "nonexistent (UEFI)"); err == nil {
		t.Fatal("SetDefault to a non-menuentry must error")
	}
}

func TestLockMutualExclusionAndStaleSteal(t *testing.T) {
	root := writeGrub(t)
	now := time.Now()
	clock := func() time.Time { return now }

	rel, err := AcquireLock(root, "hostA:1", clock)
	if err != nil {
		t.Fatal(err)
	}
	// Second acquire while held → refused.
	if _, err := AcquireLock(root, "hostB:2", clock); err == nil {
		t.Fatal("second AcquireLock should be refused while held")
	}
	rel() // release
	// Now acquirable again.
	rel2, err := AcquireLock(root, "hostB:2", clock)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	rel2()

	// Stale steal: hold, then advance the clock past TTL → another may steal.
	if _, err := AcquireLock(root, "hostA:1", clock); err != nil {
		t.Fatal(err)
	}
	future := func() time.Time { return now.Add(lockTTL + time.Minute) }
	if _, err := AcquireLock(root, "hostC:3", future); err != nil {
		t.Fatalf("stale lock should be steal-able: %v", err)
	}
}
