package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEndpointRoundTrip(t *testing.T) {
	cases := []struct {
		ip   [4]byte
		port uint16
	}{
		{[4]byte{10, 32, 0, 202}, 80},     // team lab VM
		{[4]byte{10, 32, 2, 25}, 3299},    // dev box
		{[4]byte{192, 168, 1, 150}, 3001}, // embedded pxeserver
	}
	for _, c := range cases {
		ip, port, ok := decodeEndpoint(encodeEndpoint(c.ip, c.port))
		if !ok || ip != c.ip || port != c.port {
			t.Fatalf("round-trip %v:%d -> %v:%d ok=%v", c.ip, c.port, ip, port, ok)
		}
	}
	// all-zero address is not a valid endpoint
	if _, _, ok := decodeEndpoint([6]byte{0, 0, 0, 0, 0x0b, 0xb8}); ok {
		t.Fatal("zero IPv4 must decode ok=false")
	}
}

func TestParseAdvertise(t *testing.T) {
	ip, port, err := ParseAdvertise("10.32.0.202:80")
	if err != nil || ip != [4]byte{10, 32, 0, 202} || port != 80 {
		t.Fatalf("got %v:%d err %v", ip, port, err)
	}
	for _, bad := range []string{"nope", "10.32.0.202", "::1:80", "10.0.0.1:0", "10.0.0.1:99999"} {
		if _, _, err := ParseAdvertise(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

// fakeGate captures gate + endpoint writes for assertions.
type fakeGate struct {
	endpoints []struct {
		host string
		ip   [4]byte
		port uint16
	}
}

func (f *fakeGate) WriteGate(context.Context, Node) error { return nil }
func (f *fakeGate) WriteEndpoint(_ context.Context, n Node, ip [4]byte, port uint16) error {
	f.endpoints = append(f.endpoints, struct {
		host string
		ip   [4]byte
		port uint16
	}{n.Hostname, ip, port})
	return nil
}

// With an advertise address configured, the driver stamps its endpoint into
// each node's BMC at deploy power-on.
func TestDeployStampsEndpoint(t *testing.T) {
	m := newSettleManager(t)
	fg := &fakeGate{}
	m.SetGateWriter(fg)
	m.SetAdvertise([4]byte{10, 32, 0, 202}, 80)
	if _, err := m.Start("cm", []Node{{Hostname: "m", MachineID: "1"}}, "m", nil, false, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "cm", "m", StateImaged)
	if len(fg.endpoints) == 0 {
		t.Fatal("no endpoint stamped at deploy power-on")
	}
	e := fg.endpoints[0]
	if e.ip != [4]byte{10, 32, 0, 202} || e.port != 80 {
		t.Fatalf("stamped %v:%d, want 10.32.0.202:80", e.ip, e.port)
	}
}

// No advertise address → no stamping (default behavior unchanged).
func TestDeployNoStampWithoutAdvertise(t *testing.T) {
	m := newSettleManager(t)
	fg := &fakeGate{}
	m.SetGateWriter(fg)
	if _, err := m.Start("cm", []Node{{Hostname: "m", MachineID: "1"}}, "m", nil, false, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "cm", "m", StateImaged)
	if len(fg.endpoints) != 0 {
		t.Fatalf("stamped %d endpoints without advertise set", len(fg.endpoints))
	}
}

// fakeFlipper records flip requests and lets a test force a lock conflict.
type fakeFlipper struct {
	flipped  []string
	armArgs  []string
	restored int
	failWith error
}

func (f *fakeFlipper) Flip(image, armArgs string) (func(), error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	f.flipped = append(f.flipped, image)
	f.armArgs = append(f.armArgs, armArgs)
	return func() { f.restored++ }, nil
}

// A picked image flips the PXE default at deploy start and restores once all
// nodes have booted.
func TestDeployFlipsAndRestoresImage(t *testing.T) {
	m := newSettleManager(t)
	ff := &fakeFlipper{}
	m.SetPXEFlipper(ff)
	if _, err := m.Start("cm", []Node{{Hostname: "m", MachineID: "1"}}, "m", nil, false, "v3.1.0-rc6 (UEFI)"); err != nil {
		t.Fatal(err)
	}
	if len(ff.flipped) != 1 || ff.flipped[0] != "v3.1.0-rc6 (UEFI)" {
		t.Fatalf("flipped = %v, want the picked image", ff.flipped)
	}
	// A deploy arms the entry for unattended install.
	if len(ff.armArgs) != 1 || !strings.Contains(ff.armArgs[0], "autoinstall") {
		t.Fatalf("armArgs = %v, want autoinstall", ff.armArgs)
	}
	// FakeExecutor drives the node to imaged then it awaits checkin; the restore
	// watcher fires once nodes reach preflight — simulate by reporting preflight.
	m.PreflightReport("cm", "m", NodePreflight{CarrierOK: true, Passed: true})
	waitUntil(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return ff.restored == 1 })
}

// A locked PXE default (another deploy booting) aborts the start.
func TestDeployAbortsWhenPXELocked(t *testing.T) {
	m := newSettleManager(t)
	m.SetPXEFlipper(&fakeFlipper{failWith: errors.New("PXE default is busy")})
	if _, err := m.Start("cm", []Node{{Hostname: "m", MachineID: "1"}}, "m", nil, false, "other (UEFI)"); err == nil {
		t.Fatal("Start should abort when the PXE default is locked")
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
