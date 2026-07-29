package orchestrator

import (
	"context"
	"testing"
)

func TestEndpointRoundTrip(t *testing.T) {
	cases := []struct {
		ip   [4]byte
		port uint16
	}{
		{[4]byte{10, 32, 0, 202}, 80},    // team lab VM
		{[4]byte{10, 32, 2, 25}, 3299},   // dev box
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
	if _, err := m.Start("cm", []Node{{Hostname: "m", MachineID: "1"}}, "m", nil, false); err != nil {
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
	if _, err := m.Start("cm", []Node{{Hostname: "m", MachineID: "1"}}, "m", nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "cm", "m", StateImaged)
	if len(fg.endpoints) != 0 {
		t.Fatalf("stamped %d endpoints without advertise set", len(fg.endpoints))
	}
}
