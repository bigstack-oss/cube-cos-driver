package discovery

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bigstack-oss/cube-cos-driver/internal/inventory"
	"github.com/stmcginnis/gofish"
)

type fake struct {
	inv inventory.Inventory
	err error
}

func (f fake) Discover(context.Context, Target) (inventory.Inventory, error) {
	return f.inv, f.err
}

func TestCombinedPrefersPrimaryWhenRich(t *testing.T) {
	c := Combined{
		Primary:   fake{inv: inventory.Inventory{Source: "redfish", CPUCount: 2}},
		Secondary: fake{inv: inventory.Inventory{Source: "ipmi", Serial: "SN"}},
	}
	inv, err := c.Discover(context.Background(), Target{})
	if err != nil || inv.Source != "redfish" {
		t.Fatalf("expected redfish, got %+v err %v", inv, err)
	}
}

func TestCombinedFallsBackOnPrimaryError(t *testing.T) {
	c := Combined{
		Primary:   fake{err: errors.New("no redfish")},
		Secondary: fake{inv: inventory.Inventory{Source: "ipmi", Serial: "SN"}},
	}
	inv, err := c.Discover(context.Background(), Target{})
	if err != nil || inv.Source != "ipmi" {
		t.Fatalf("expected ipmi fallback, got %+v err %v", inv, err)
	}
}

func TestCombinedFallsBackOnEmptyPrimary(t *testing.T) {
	c := Combined{
		Primary:   fake{inv: inventory.Inventory{Source: "redfish"}}, // no core facts
		Secondary: fake{inv: inventory.Inventory{Source: "ipmi", Serial: "SN"}},
	}
	inv, err := c.Discover(context.Background(), Target{})
	if err != nil || inv.Source != "ipmi" {
		t.Fatalf("expected ipmi fallback on empty primary, got %+v err %v", inv, err)
	}
}

func TestCombinedReturnsErrorWhenBothFail(t *testing.T) {
	c := Combined{
		Primary:   fake{err: errors.New("no redfish")},
		Secondary: fake{err: errors.New("no ipmi")},
	}
	if _, err := c.Discover(context.Background(), Target{}); err == nil {
		t.Fatal("expected error when both fail")
	}
}

func TestRaidLabel(t *testing.T) {
	cases := map[[2]string]string{
		{"RAID6", ""}:        "RAID6",       // modern RAIDType wins
		{"", "Mirrored"}:     "RAID1",       // iDRAC8 legacy VolumeType
		{"", "NonRedundant"}: "RAID0",
		{"", "Weird"}:        "Weird",       // unknown passes through
	}
	for in, want := range cases {
		if got := raidLabel(in[0], in[1]); got != want {
			t.Errorf("raidLabel(%q,%q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

// The real iDRAC8 failure: the Volumes collection mixes a RAID-1 VD with
// pass-through "RawDevice" volumes that send "Encrypted":null. gofish's typed
// parse chokes on the null and drops the whole collection; fetchVirtualDisks
// reads it tolerantly, returns only the VD, and skips the pass-throughs.
func TestFetchVirtualDisksIDRAC8(t *testing.T) {
	const storageURL = "/redfish/v1/Systems/System.Embedded.1/Storage/RAID.Integrated.1-1"
	routes := map[string]string{
		"/redfish/v1/": `{"@odata.id":"/redfish/v1/","@odata.type":"#ServiceRoot.v1_0_0.ServiceRoot","Id":"RootService","Name":"Root Service"}`,
		storageURL:     `{"@odata.id":"` + storageURL + `","Volumes":{"@odata.id":"` + storageURL + `/Volumes"}}`,
		storageURL + "/Volumes": `{"Members":[
			{"@odata.id":"/vd0"},{"@odata.id":"/raw2"}]}`,
		"/vd0":  `{"Name":"CubeCOS","VolumeType":"Mirrored","Encrypted":false,"CapacityBytes":799535005696,"Links":{"Drives":[{"@odata.id":"/d0"},{"@odata.id":"/d1"}]}}`,
		"/raw2": `{"Name":"Solid State Disk 0:1:2","VolumeType":"RawDevice","Encrypted":null,"OptimumIOSizeBytes":null,"CapacityBytes":799535005696}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := routes[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client, err := gofish.ConnectDefault(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Logout()

	disks, members := fetchVirtualDisks(client, storageURL)
	if len(disks) != 1 {
		t.Fatalf("got %d disks, want 1 (the VD; RawDevice skipped): %+v", len(disks), disks)
	}
	d := disks[0]
	if d.Name != "CubeCOS" || d.Type != "RAID1" || d.SizeBytes != 799535005696 {
		t.Fatalf("VD mapped wrong: %+v", d)
	}
	if d.OSEligible == nil || !*d.OSEligible {
		t.Fatal("VD must be explicitly OS-eligible")
	}
	if len(members) != 2 || !members["/d0"] || !members["/d1"] {
		t.Fatalf("member refs must be the VD's Links.Drives only, got %v", members)
	}
}

// When the BMC honors $expand, member detail is inlined in the collection and
// no per-member GET is issued (one round-trip against a slow controller).
func TestFetchVirtualDisksExpanded(t *testing.T) {
	const storageURL = "/redfish/v1/Systems/System.Embedded.1/Storage/RAID.Integrated.1-1"
	var memberGets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/redfish/v1/":
			w.Write([]byte(`{"@odata.id":"/redfish/v1/","Id":"RootService","Name":"Root Service"}`))
		case r.URL.Path == storageURL+"/Volumes" && r.URL.RawQuery != "":
			// $expand — inline the members
			w.Write([]byte(`{"Members":[
				{"@odata.id":"/vd0","Name":"CubeCOS","VolumeType":"Mirrored","CapacityBytes":799535005696},
				{"@odata.id":"/raw2","Name":"SSD 2","VolumeType":"RawDevice"}]}`))
		default:
			memberGets++
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := gofish.ConnectDefault(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Logout()

	disks, _ := fetchVirtualDisks(client, storageURL)
	if len(disks) != 1 || disks[0].Name != "CubeCOS" {
		t.Fatalf("want just the CubeCOS VD, got %+v", disks)
	}
	if memberGets != 0 {
		t.Fatalf("expanded members must not be re-fetched (%d per-member GETs)", memberGets)
	}
}

// discoverRoutes is a minimal Redfish tree a Discover walk can complete on.
func discoverRoutes() map[string]string {
	const sysURL = "/redfish/v1/Systems/S1"
	return map[string]string{
		"/redfish/v1/":        `{"@odata.id":"/redfish/v1/","Id":"RootService","Name":"Root Service","Systems":{"@odata.id":"/redfish/v1/Systems"}}`,
		"/redfish/v1/Systems": `{"Members":[{"@odata.id":"` + sysURL + `"}],"Members@odata.count":1}`,
		sysURL:                `{"@odata.id":"` + sysURL + `","Id":"S1","Name":"S1","Manufacturer":"Dell","Model":"R630","SerialNumber":"SN1","EthernetInterfaces":{"@odata.id":"` + sysURL + `/EthernetInterfaces"}}`,
		sysURL + "/EthernetInterfaces": `{"Members":[{"@odata.id":"/n0"},{"@odata.id":"/n1"},{"@odata.id":"/n2"},{"@odata.id":"/n3"},{"@odata.id":"/n4"},{"@odata.id":"/n5"}],"Members@odata.count":6}`,
		"/n0":                          `{"@odata.id":"/n0","Id":"n0","Name":"NIC0","MACAddress":"02:00:00:00:00:00"}`,
		"/n1":                          `{"@odata.id":"/n1","Id":"n1","Name":"NIC1","MACAddress":"02:00:00:00:00:01"}`,
		"/n2":                          `{"@odata.id":"/n2","Id":"n2","Name":"NIC2","MACAddress":"02:00:00:00:00:02"}`,
		"/n3":                          `{"@odata.id":"/n3","Id":"n3","Name":"NIC3","MACAddress":"02:00:00:00:00:03"}`,
		"/n4":                          `{"@odata.id":"/n4","Id":"n4","Name":"NIC4","MACAddress":"02:00:00:00:00:04"}`,
		"/n5":                          `{"@odata.id":"/n5","Id":"n5","Name":"NIC5","MACAddress":"02:00:00:00:00:05"}`,
	}
}

// Old BMC firmware (Dell iDRAC8) offers only RSA-key-exchange TLS cipher
// suites, which Go 1.22+ removed from the client defaults — discovery must
// still connect (the "inspect fails at TLS handshake" bug).
func TestDiscoverRSAKeyExchangeTLS(t *testing.T) {
	routes := discoverRoutes()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := routes[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	srv.TLS = &tls.Config{
		MaxVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{tls.TLS_RSA_WITH_AES_256_GCM_SHA384},
	}
	srv.StartTLS()
	defer srv.Close()

	inv, err := RedfishDiscoverer{}.Discover(context.Background(), Target{Address: srv.URL})
	if err != nil {
		t.Fatalf("Discover over RSA-KEX-only TLS: %v", err)
	}
	if inv.Serial != "SN1" {
		t.Fatalf("unexpected inventory: %+v", inv)
	}
}

// Discovery must issue requests concurrently — a BMC at ~5s/request makes a
// fully serial ~20-request walk take minutes (the "inspect takes too long"
// bug). Watch the fake BMC's in-flight high-water mark.
func TestDiscoverRequestsConcurrently(t *testing.T) {
	routes := discoverRoutes()
	var inflight, peak int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&inflight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)
		if body, ok := routes[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	inv, err := RedfishDiscoverer{}.Discover(context.Background(), Target{Address: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.NICs) != 6 {
		t.Fatalf("want 6 NICs, got %+v", inv.NICs)
	}
	if p := atomic.LoadInt32(&peak); p < 2 {
		t.Fatalf("requests never overlapped (peak in-flight %d) — discovery is serial", p)
	}
}

// Only the drives listed in a VD's Links.Drives are RAID members. Drives on the
// same controller that are not in any VD (pass-throughs, unconfigured) stay
// OS-visible and must not be marked ineligible (the "every disk shows as RAID
// member" bug).
func TestDiscoverMarksOnlyVolumeMemberDrives(t *testing.T) {
	const sysURL = "/redfish/v1/Systems/S1"
	const stURL = sysURL + "/Storage/C1"
	drive := func(id, name string) string {
		return `{"@odata.id":"` + id + `","Id":"` + name + `","Name":"` + name + `","Model":"SSDSC2KG480G8R","MediaType":"SSD","CapacityBytes":480103981056}`
	}
	routes := map[string]string{
		"/redfish/v1/":        `{"@odata.id":"/redfish/v1/","Id":"RootService","Name":"Root Service","Systems":{"@odata.id":"/redfish/v1/Systems"}}`,
		"/redfish/v1/Systems": `{"Members":[{"@odata.id":"` + sysURL + `"}],"Members@odata.count":1}`,
		sysURL:                `{"@odata.id":"` + sysURL + `","Id":"S1","Name":"S1","Manufacturer":"Dell","Model":"R630","SerialNumber":"SN1","Storage":{"@odata.id":"` + sysURL + `/Storage"}}`,
		sysURL + "/Storage":   `{"Members":[{"@odata.id":"` + stURL + `"}],"Members@odata.count":1}`,
		stURL: `{"@odata.id":"` + stURL + `","Id":"C1","Name":"PERC","Drives":[
			{"@odata.id":"/d0"},{"@odata.id":"/d1"},{"@odata.id":"/d2"},{"@odata.id":"/d3"}],
			"Volumes":{"@odata.id":"` + stURL + `/Volumes"}}`,
		stURL + "/Volumes": `{"Members":[{"@odata.id":"/vd0"},{"@odata.id":"/raw2"}]}`,
		"/vd0":             `{"@odata.id":"/vd0","Name":"cubecos","VolumeType":"Mirrored","Encrypted":false,"CapacityBytes":479559942144,"Links":{"Drives":[{"@odata.id":"/d0"},{"@odata.id":"/d1"}]}}`,
		"/raw2":            `{"@odata.id":"/raw2","Name":"Solid State Disk 0:1:2","VolumeType":"RawDevice","Encrypted":null,"Links":{"Drives":[{"@odata.id":"/d2"}]}}`,
		"/d0":              drive("/d0", "Solid State Disk 0:1:0"),
		"/d1":              drive("/d1", "Solid State Disk 0:1:1"),
		"/d2":              drive("/d2", "Solid State Disk 0:1:2"),
		"/d3":              drive("/d3", "Solid State Disk 0:1:3"),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := routes[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	inv, err := RedfishDiscoverer{}.Discover(context.Background(), Target{Address: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	eligible := map[string]string{} // name → "true" | "false" | "unset"
	for _, d := range inv.Disks {
		switch {
		case d.OSEligible == nil:
			eligible[d.Name] = "unset"
		case *d.OSEligible:
			eligible[d.Name] = "true"
		default:
			eligible[d.Name] = "false"
		}
	}
	want := map[string]string{
		"cubecos":                "true",  // the VD is the install target
		"Solid State Disk 0:1:0": "false", // RAID member (in vd0's Links.Drives)
		"Solid State Disk 0:1:1": "false", // RAID member
		"Solid State Disk 0:1:2": "unset", // pass-through — OS-visible
		"Solid State Disk 0:1:3": "unset", // not in any VD — OS-visible
	}
	if len(inv.Disks) != len(want) {
		t.Fatalf("got %d disks, want %d: %+v", len(inv.Disks), len(want), inv.Disks)
	}
	for name, w := range want {
		if eligible[name] != w {
			t.Errorf("disk %q OSEligible = %s, want %s", name, eligible[name], w)
		}
	}
}
