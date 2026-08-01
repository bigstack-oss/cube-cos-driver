package discovery

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bigstack-oss/cube-cos-driver/internal/inventory"
	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"
)

// RedfishDiscoverer reads hardware facts over the Redfish API (gofish).
type RedfishDiscoverer struct{}

// bmcConcurrency caps in-flight requests per BMC. Slow controllers (Dell
// iDRAC8 is ~5s/request) make a serial ~20-request walk take minutes; a few
// parallel requests cut that without overwhelming the BMC (gofish's own
// collection fan-out is 3-wide).
const bmcConcurrency = 4

// bmcHTTPClient permits the RSA-key-exchange TLS cipher suites that old BMC
// firmware (e.g. Dell iDRAC8) is limited to — Go 1.22+ dropped them from the
// client defaults, failing the handshake outright.
func bmcHTTPClient() *http.Client {
	var suites []uint16
	for _, s := range tls.CipherSuites() {
		suites = append(suites, s.ID)
	}
	suites = append(suites,
		tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
	)
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{CipherSuites: suites},
	}}
}

func (RedfishDiscoverer) Discover(ctx context.Context, t Target) (inventory.Inventory, error) {
	endpoint := t.Address
	if len(endpoint) < 4 || endpoint[:4] != "http" {
		endpoint = "https://" + endpoint
	}
	client, err := gofish.ConnectContext(ctx, gofish.ClientConfig{
		Endpoint:              endpoint,
		Username:              t.Username,
		Password:              t.Password,
		Insecure:              true, // BMC certs are self-signed
		BasicAuth:             true,
		HTTPClient:            bmcHTTPClient(),
		MaxConcurrentRequests: bmcConcurrency,
	})
	if err != nil {
		return inventory.Inventory{}, err
	}
	defer client.Logout()

	systems, err := client.Service.Systems()
	if err != nil || len(systems) == 0 {
		if err == nil {
			err = errRedfishNoSystems
		}
		return inventory.Inventory{}, err
	}
	sys := systems[0]

	inv := inventory.Inventory{
		FetchedAt:    time.Now().UTC().Format(time.RFC3339),
		Source:       "redfish",
		Manufacturer: sys.Manufacturer,
		Model:        sys.Model,
		Serial:       sys.SerialNumber,
		CPUModel:     sys.ProcessorSummary.Model,
	}
	if sys.ProcessorSummary.Count != nil {
		inv.CPUCount = int(*sys.ProcessorSummary.Count)
	}
	if sys.ProcessorSummary.CoreCount != nil {
		inv.CPUCores = int(*sys.ProcessorSummary.CoreCount)
	}
	if sys.MemorySummary.TotalSystemMemoryGiB != nil {
		inv.MemoryBytes = int64(*sys.MemorySummary.TotalSystemMemoryGiB * (1 << 30))
	}

	// The three sections are independent — walk them concurrently (the BMC is
	// the bottleneck; the client caps total in-flight at bmcConcurrency).
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		eths, err := sys.EthernetInterfaces()
		if err != nil {
			return
		}
		for _, e := range eths {
			mac := e.MACAddress
			if mac == "" {
				mac = e.PermanentMACAddress
			}
			nic := inventory.NIC{
				Name: e.Name,
				MAC:  mac,
				Up:   e.LinkStatus == schemas.LinkUpLinkStatus,
			}
			if e.SpeedMbps != nil {
				nic.SpeedMbps = *e.SpeedMbps
			}
			inv.NICs = append(inv.NICs, nic)
		}
	}()

	go func() {
		defer wg.Done()
		storages, err := sys.Storage()
		if err != nil {
			return
		}
		for _, st := range storages {
			// RAID virtual disks first: on a HW-RAID controller (e.g. Dell PERC)
			// the OS sees the virtual disk, not its member drives — so VDs are
			// the OS-install candidates and the members are not.
			vds, members := fetchVirtualDisks(client, st.ODataID)
			inv.Disks = append(inv.Disks, vds...)
			drives, err := st.Drives()
			if err != nil {
				continue
			}
			for _, d := range drives {
				disk := inventory.Disk{
					Name:  d.Name,
					Model: d.Model,
					Type:  string(d.MediaType),
				}
				if d.CapacityBytes != nil {
					disk.SizeBytes = int64(*d.CapacityBytes)
				}
				// A drive inside a VD is hidden from the OS — never an install
				// target. Drives outside every VD (pass-through, unconfigured)
				// stay OS-visible. If the BMC lists VDs but no membership,
				// membership is unknown: keep the old conservative marking.
				if members[d.ODataID] || (len(vds) > 0 && len(members) == 0) {
					no := false
					disk.OSEligible = &no
				}
				inv.Disks = append(inv.Disks, disk)
			}
		}
	}()

	go func() {
		defer wg.Done()
		devices, err := sys.PCIeDevices()
		if err != nil {
			return
		}
		for _, dev := range devices {
			inv.Cards = append(inv.Cards, inventory.Card{
				Name: dev.Name,
				Type: string(dev.DeviceType),
			})
		}
	}()

	wg.Wait()
	return inv, nil
}

// rfVolume is the subset of a Redfish Volume we need.
type rfVolume struct {
	ODataID       string `json:"@odata.id"`
	Name          string `json:"Name"`
	DisplayName   string `json:"DisplayName"`
	Description   string `json:"Description"`
	CapacityBytes *int64 `json:"CapacityBytes"`
	VolumeType    string `json:"VolumeType"`
	RAIDType      string `json:"RAIDType"`
	Links         struct {
		Drives []struct {
			ODataID string `json:"@odata.id"`
		} `json:"Drives"`
	} `json:"Links"`
}

func (v rfVolume) unresolved() bool {
	return v.Name == "" && v.VolumeType == "" && v.RAIDType == ""
}

// fetchVirtualDisks returns a controller's RAID virtual disks as OS-install
// candidates, plus the @odata.id set of their member drives (Links.Drives) so
// the caller can mark exactly those drives as RAID members. It parses the
// Volumes collection from raw JSON (gofish's typed Volume rejects Dell
// iDRAC8's "Encrypted":null and drops the whole set) and skips Dell
// "RawDevice" pass-throughs (the physical drives, already reported).
func fetchVirtualDisks(client *gofish.APIClient, storageURL string) ([]inventory.Disk, map[string]bool) {
	var out []inventory.Disk
	members := map[string]bool{}
	if storageURL == "" {
		return out, members
	}
	// Volumes is a standard sub-resource of Storage. Ask the controller to
	// inline the members ($expand) so a slow BMC (Dell iDRAC8 is ~5s/request)
	// costs one round-trip, not one per volume. Fall back to the plain
	// collection if $expand is unsupported; unexpanded members are fetched
	// individually below.
	var coll struct {
		Members []rfVolume `json:"Members"`
	}
	if err := getJSON(client, storageURL+"/Volumes?$expand=*($levels=1)", &coll); err != nil || len(coll.Members) == 0 {
		coll.Members = nil
		if err := getJSON(client, storageURL+"/Volumes", &coll); err != nil {
			return out, members // no Volumes resource (e.g. an AHCI passthrough controller)
		}
	}
	// $expand ignored — resolve the volumes individually, in parallel (the
	// client's request cap bounds actual concurrency against the BMC).
	var wg sync.WaitGroup
	for i := range coll.Members {
		if v := &coll.Members[i]; v.unresolved() && v.ODataID != "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = getJSON(client, v.ODataID, v)
			}()
		}
	}
	wg.Wait()
	for _, v := range coll.Members {
		if strings.EqualFold(v.VolumeType, "RawDevice") {
			continue // a pass-through of a single physical disk, not a VD
		}
		if v.unresolved() {
			continue
		}
		for _, m := range v.Links.Drives {
			if m.ODataID != "" {
				members[m.ODataID] = true
			}
		}
		name := firstNonEmpty(v.DisplayName, v.Name, v.Description)
		raid := raidLabel(v.RAIDType, v.VolumeType)
		yes := true
		d := inventory.Disk{
			Name:       name,
			Model:      strings.TrimSpace("RAID virtual disk " + raid),
			Type:       raid,
			OSEligible: &yes,
		}
		if v.CapacityBytes != nil {
			d.SizeBytes = *v.CapacityBytes
		}
		out = append(out, d)
	}
	return out, members
}

// getJSON GETs a Redfish resource and decodes it into v.
func getJSON(client *gofish.APIClient, url string, v any) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// raidLabel prefers the modern RAIDType, falling back to the legacy VolumeType
// (iDRAC8) mapped to a familiar RAID level.
func raidLabel(raidType, volumeType string) string {
	if raidType != "" {
		return raidType
	}
	switch volumeType {
	case "Mirrored":
		return "RAID1"
	case "NonRedundant":
		return "RAID0"
	case "StripedWithParity":
		return "RAID5"
	case "SpannedMirrors":
		return "RAID10"
	case "SpannedStripesWithParity":
		return "RAID60"
	default:
		return volumeType
	}
}
