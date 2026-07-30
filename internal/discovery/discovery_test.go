package discovery

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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

	disks := fetchVirtualDisks(client, storageURL)
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

	disks := fetchVirtualDisks(client, storageURL)
	if len(disks) != 1 || disks[0].Name != "CubeCOS" {
		t.Fatalf("want just the CubeCOS VD, got %+v", disks)
	}
	if memberGets != 0 {
		t.Fatalf("expanded members must not be re-fetched (%d per-member GETs)", memberGets)
	}
}
