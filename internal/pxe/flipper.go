package pxe

import (
	"fmt"
	"os"
	"time"
)

// Flipper implements orchestrator.PXEFlipper against a PXE root (grub.cfg dir).
// It repoints the default to a chosen image under the shared advisory lock and
// returns a restore func that sets the previous default back and releases the
// lock.
type Flipper struct {
	Root   string // grub.cfg directory (local, or an NFS mount of the PXE export)
	Holder string // this driver's identity recorded in the lock (host:pid)
}

// NewFlipper builds a Flipper; holder defaults to hostname:pid.
func NewFlipper(root string) *Flipper {
	host, _ := os.Hostname()
	return &Flipper{Root: root, Holder: fmt.Sprintf("%s:%d", host, os.Getpid())}
}

// Flip repoints the PXE default to image. If image is already the default it is
// a no-op (nil restore, nil error). Otherwise it takes the advisory lock,
// records the current default, sets the new one, and returns a restore func.
func (f *Flipper) Flip(image string) (func(), error) {
	cur, err := CurrentDefault(f.Root)
	if err != nil {
		return nil, err
	}
	if image == cur {
		return nil, nil // already booting this image — nothing to flip or lock
	}
	release, err := AcquireLock(f.Root, f.Holder, time.Now)
	if err != nil {
		return nil, err
	}
	if err := SetDefault(f.Root, image); err != nil {
		release()
		return nil, err
	}
	var once bool
	return func() {
		if once {
			return
		}
		once = true
		if err := SetDefault(f.Root, cur); err != nil {
			// Best effort: log via caller; still release so we don't wedge.
			fmt.Fprintf(os.Stderr, "pxe: restore default to %q: %v\n", cur, err)
		}
		release()
	}, nil
}
