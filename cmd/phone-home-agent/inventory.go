package main

import (
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type nicInv struct {
	Name      string `json:"name"`
	MAC       string `json:"mac"`
	PCIAddr   string `json:"pciAddr"`   // PCI bus id, e.g. 0000:01:00.0
	PCIVendor string `json:"pciVendor"` // PCI vendor id, e.g. 0x8086
	SpeedMbps int    `json:"speedMbps"`
	Up        bool   `json:"up"`      // interface operstate up
	Carrier   bool   `json:"carrier"` // physical link (cable) up
}

type diskInv struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	SizeBytes  int64  `json:"sizeBytes"`
	Type       string `json:"type"` // HDD | SSD | NVMe
	Tran       string `json:"tran"` // transport: sata | sas | nvme | iscsi | fc | usb
	OSEligible bool   `json:"osEligible"`
}

// diskEligible reports whether a disk is a valid CubeCOS OS-install target: a
// local physical disk, never a SAN LUN (iSCSI/FC, e.g. Dell Compellent) or BMC
// virtual media. Keyed on transport first (authoritative) with model markers as
// a backstop for SAN arrays reached over a masked transport.
func diskEligible(name, model, tran string, size int64) bool {
	if size <= 0 {
		return false
	}
	switch strings.ToLower(tran) {
	case "iscsi", "fc", "fcoe", "usb":
		return false
	}
	m := strings.ToLower(model + " " + name)
	for _, bad := range []string{
		"virtual", "idrac", "vdvd", "dvd", "cd-rom", "cdrom",
		"compellent", "3par", "msa", "nimble", "unity", "powerstore", "vnx", "netapp", "eternus",
	} {
		if strings.Contains(m, bad) {
			return false
		}
	}
	return true
}

type invReport struct {
	Serial       string    `json:"serial"`
	MACs         []string  `json:"macs"`
	Manufacturer string    `json:"manufacturer"`
	Model        string    `json:"model"`
	CPUModel     string    `json:"cpuModel"`
	CPUCount     int       `json:"cpuCount"`
	MemoryBytes  int64     `json:"memoryBytes"`
	NICs         []nicInv  `json:"nics"`
	Disks        []diskInv `json:"disks"`
}

// reportInventory (inspect boot) reads this node's hardware, reports it to the
// server, and powers the node off — so an operator has CPU/mem/disk/NIC to drive
// the assign flow (NIC mapper + OS-disk picker) without needing Redfish.
func reportInventory(srv string) {
	rep := gatherInventory()
	log.Printf("inventory: serial=%s cpu=%q x%d mem=%dGB nics=%d disks=%d",
		rep.Serial, rep.CPUModel, rep.CPUCount, rep.MemoryBytes/(1<<30), len(rep.NICs), len(rep.Disks))
	postJSON(srv+"/api/v1/machines/inventory-report", rep)
	log.Printf("inventory: reported; powering off in 5s")
	time.Sleep(5 * time.Second)
	_ = exec.Command("systemctl", "poweroff").Run()
	_ = exec.Command("poweroff", "-f").Run()
}

func gatherInventory() invReport {
	r := invReport{Serial: serial(), MACs: macs()}
	r.Manufacturer = strings.TrimSpace(readFileStr("/sys/class/dmi/id/sys_vendor"))
	r.Model = strings.TrimSpace(readFileStr("/sys/class/dmi/id/product_name"))
	r.CPUModel, r.CPUCount = cpuInfo()
	r.MemoryBytes = memTotalBytes()
	r.NICs = nicsInv()
	r.Disks = disksInv()
	return r
}

func readFileStr(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func cpuInfo() (model string, count int) {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "", 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "processor") {
			count++
		} else if model == "" && strings.HasPrefix(line, "model name") {
			if i := strings.Index(line, ":"); i >= 0 {
				model = strings.TrimSpace(line[i+1:])
			}
		}
	}
	return model, count
}

func memTotalBytes() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kb, _ := strconv.ParseInt(f[1], 10, 64)
				return kb * 1024
			}
		}
	}
	return 0
}

func nicsInv() []nicInv {
	var out []nicInv
	entries, _ := os.ReadDir("/sys/class/net")
	for _, e := range entries {
		n := e.Name()
		if n == "lo" {
			continue
		}
		mac := strings.TrimSpace(readFileStr("/sys/class/net/" + n + "/address"))
		if mac == "" || mac == "00:00:00:00:00:00" {
			continue
		}
		speed := 0
		if s := strings.TrimSpace(readFileStr("/sys/class/net/" + n + "/speed")); s != "" {
			if v, err := strconv.Atoi(s); err == nil && v > 0 {
				speed = v
			}
		}
		up := strings.TrimSpace(readFileStr("/sys/class/net/" + n + "/operstate")) == "up"
		carrier := strings.TrimSpace(readFileStr("/sys/class/net/"+n+"/carrier")) == "1"
		pci, vendor := "", ""
		if dest, err := os.Readlink("/sys/class/net/" + n + "/device"); err == nil {
			pci = dest[strings.LastIndex(dest, "/")+1:]
		}
		vendor = strings.TrimSpace(readFileStr("/sys/class/net/" + n + "/device/vendor"))
		out = append(out, nicInv{
			Name: n, MAC: mac, PCIAddr: pci, PCIVendor: vendor,
			SpeedMbps: speed, Up: up, Carrier: carrier,
		})
	}
	return out
}

func disksInv() []diskInv {
	out := []diskInv{}
	b, err := exec.Command("lsblk", "-bJ", "-o", "NAME,SIZE,MODEL,ROTA,TYPE,TRAN").Output()
	if err != nil {
		return out
	}
	var parsed struct {
		BlockDevices []struct {
			Name  string `json:"name"`
			Size  int64  `json:"size"`
			Model string `json:"model"`
			Rota  bool   `json:"rota"`
			Type  string `json:"type"`
			Tran  string `json:"tran"`
		} `json:"blockdevices"`
	}
	if json.Unmarshal(b, &parsed) != nil {
		return out
	}
	for _, d := range parsed.BlockDevices {
		if d.Type != "disk" {
			continue
		}
		t := "HDD"
		if strings.EqualFold(d.Tran, "nvme") || strings.HasPrefix(d.Name, "nvme") {
			t = "NVMe"
		} else if !d.Rota {
			t = "SSD"
		}
		name := "/dev/" + d.Name
		model := strings.TrimSpace(d.Model)
		out = append(out, diskInv{
			Name: name, Model: model, SizeBytes: d.Size, Type: t, Tran: d.Tran,
			OSEligible: diskEligible(name, model, d.Tran, d.Size),
		})
	}
	return out
}
