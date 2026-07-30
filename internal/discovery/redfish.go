package discovery

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/bigstack-oss/cube-cos-driver/internal/inventory"
	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"
)

// RedfishDiscoverer reads hardware facts over the Redfish API (gofish).
type RedfishDiscoverer struct{}

func (RedfishDiscoverer) Discover(ctx context.Context, t Target) (inventory.Inventory, error) {
	endpoint := t.Address
	if len(endpoint) < 4 || endpoint[:4] != "http" {
		endpoint = "https://" + endpoint
	}
	client, err := gofish.ConnectContext(ctx, gofish.ClientConfig{
		Endpoint:  endpoint,
		Username:  t.Username,
		Password:  t.Password,
		Insecure:  true, // BMC certs are self-signed
		BasicAuth: true,
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

	if eths, err := sys.EthernetInterfaces(); err == nil {
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
	}

	if storages, err := sys.Storage(); err == nil {
		for _, st := range storages {
			// RAID virtual disks first: on a HW-RAID controller (e.g. Dell PERC)
			// the OS sees the virtual disk, not the member drives — so VDs are
			// the OS-install candidates and the members are not.
			hasVD := false
			for _, vd := range fetchVirtualDisks(client, st.ODataID) {
				inv.Disks = append(inv.Disks, vd)
				hasVD = true
			}
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
				if hasVD {
					// Behind a RAID controller with volumes defined: the member
					// drive is not OS-visible — never an install target.
					no := false
					disk.OSEligible = &no
				}
				inv.Disks = append(inv.Disks, disk)
			}
		}
	}

	if devices, err := sys.PCIeDevices(); err == nil {
		for _, dev := range devices {
			inv.Cards = append(inv.Cards, inventory.Card{
				Name: dev.Name,
				Type: string(dev.DeviceType),
			})
		}
	}

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
}

func (v rfVolume) unresolved() bool {
	return v.Name == "" && v.VolumeType == "" && v.RAIDType == ""
}

// fetchVirtualDisks returns a controller's RAID virtual disks as OS-install
// candidates. It parses the Volumes collection from raw JSON (gofish's typed
// Volume rejects Dell iDRAC8's "Encrypted":null and drops the whole set) and
// skips Dell "RawDevice" pass-throughs (the physical drives, already reported).
func fetchVirtualDisks(client *gofish.APIClient, storageURL string) []inventory.Disk {
	var out []inventory.Disk
	if storageURL == "" {
		return out
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
			return out // no Volumes resource (e.g. an AHCI passthrough controller)
		}
	}
	for _, v := range coll.Members {
		if v.unresolved() && v.ODataID != "" {
			_ = getJSON(client, v.ODataID, &v) // $expand ignored — resolve one by one
		}
		if strings.EqualFold(v.VolumeType, "RawDevice") {
			continue // a pass-through of a single physical disk, not a VD
		}
		if v.unresolved() {
			continue
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
	return out
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
