package discovery

import (
	"context"
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
			if vols, verr := st.Volumes(); verr == nil {
				for _, v := range vols {
					inv.Disks = append(inv.Disks, volumeDisk(v))
					hasVD = true
				}
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

// volumeDisk maps a Redfish RAID volume (virtual disk) to an inventory disk.
// Explicitly OS-eligible: the UI's model heuristic would otherwise exclude
// names containing "virtual".
func volumeDisk(v *schemas.Volume) inventory.Disk {
	name := v.DisplayName
	if name == "" {
		name = v.Name
	}
	raid := string(v.RAIDType)
	if raid == "" {
		raid = string(v.VolumeType)
	}
	yes := true
	d := inventory.Disk{
		Name:       name,
		Model:      strings.TrimSpace("RAID virtual disk " + raid),
		Type:       raid,
		OSEligible: &yes,
	}
	if v.CapacityBytes != nil {
		d.SizeBytes = int64(*v.CapacityBytes)
	}
	return d
}
