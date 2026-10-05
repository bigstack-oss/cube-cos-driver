package main

import (
	"errors"
	"testing"
	"time"
)

// A wedged in-band IPMI op returns errIPMIWedged at the deadline instead of hanging.
func TestWithIPMIBoundsWedgedOp(t *testing.T) {
	start := time.Now()
	err := withIPMI(50*time.Millisecond, func() error {
		time.Sleep(10 * time.Second) // simulates open/ioctl that never returns
		return nil
	})
	if !errors.Is(err, errIPMIWedged) {
		t.Fatalf("want errIPMIWedged, got %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("withIPMI blocked %v past its bound", d)
	}
}

// A responsive op returns its own result (nil or error) unchanged.
func TestWithIPMIPassesThroughFastResult(t *testing.T) {
	if err := withIPMI(time.Second, func() error { return nil }); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	sentinel := errors.New("bmc said no")
	if err := withIPMI(time.Second, func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel, got %v", err)
	}
}
