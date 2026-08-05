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

// Flip prepares the PXE entry for a driver-initiated boot: it points the
// default at the target image (the picked image, or the current default when
// image is "") and injects armArgs — the zero-touch arming (e.g. "autoinstall
// driver_server=http://<this-driver>") — into that entry's kernel line, all
// under the shared advisory lock. The returned restore func strips the args,
// puts the previous default back, and releases the lock. So the shared grub
// entry is armed only while THIS driver's deploy is booting.
func (f *Flipper) Flip(image, armArgs string) (func(), error) {
	prevDefault, err := CurrentDefault(f.Root)
	if err != nil {
		return nil, err
	}
	target := image
	if target == "" {
		target = prevDefault // no image picked — arm whatever boots by default
	}
	prevArgs, err := EntryArgs(f.Root, target)
	if err != nil {
		return nil, err
	}
	prevTimeout, err := CurrentTimeout(f.Root)
	if err != nil {
		return nil, err
	}
	release, err := AcquireLock(f.Root, f.Holder, time.Now)
	if err != nil {
		return nil, err
	}
	undo := func() {
		if target != prevDefault {
			if e := SetDefault(f.Root, prevDefault); e != nil {
				fmt.Fprintf(os.Stderr, "pxe: restore default to %q: %v\n", prevDefault, e)
			}
		}
		if e := SetEntryArgs(f.Root, target, prevArgs); e != nil {
			fmt.Fprintf(os.Stderr, "pxe: restore args on %q: %v\n", target, e)
		}
		if prevTimeout != "" && prevTimeout != "0" {
			if e := SetTimeout(f.Root, prevTimeout); e != nil {
				fmt.Fprintf(os.Stderr, "pxe: restore timeout to %q: %v\n", prevTimeout, e)
			}
		}
	}
	if target != prevDefault {
		if err := SetDefault(f.Root, target); err != nil {
			release()
			return nil, err
		}
	}
	if err := SetEntryArgs(f.Root, target, armArgs); err != nil {
		undo()
		release()
		return nil, err
	}
	// Force auto-boot of the armed default; without this an ambient timeout=-1
	// leaves the node stuck at the grub menu and the deploy silently hangs.
	if err := SetTimeout(f.Root, "0"); err != nil {
		undo()
		release()
		return nil, err
	}
	var once bool
	return func() {
		if once {
			return
		}
		once = true
		undo()
		release()
	}, nil
}
