// Package inventory defines the machine (BMC) records and their fetched
// hardware facts, and persists them with encrypted credentials.
package inventory

type BMC struct {
	Address  string `json:"address"`
	Username string `json:"username"`
	// Cipher pins the IPMI cipher suite (e.g. 17); 0 lets the library try all.
	Cipher int `json:"cipher,omitempty"`
}

type NIC struct {
	Name      string `json:"name,omitempty"`
	MAC       string `json:"mac,omitempty"`
	PCIAddr   string `json:"pciAddr,omitempty"`
	PCIVendor string `json:"pciVendor,omitempty"`
	SpeedMbps int    `json:"speedMbps,omitempty"`
	Up        bool   `json:"up,omitempty"`
	// Carrier (physical link up) is a pointer so inventory captured before the
	// agent reported it reads as unknown rather than a false "link down".
	Carrier *bool `json:"carrier,omitempty"`
}

type Disk struct {
	Name      string `json:"name,omitempty"`
	Model     string `json:"model,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	Type      string `json:"type,omitempty"` // HDD | SSD | NVMe
	Tran      string `json:"tran,omitempty"` // transport: sata|sas|nvme|iscsi|fc|usb
	// OSEligible: safe as a CubeCOS OS-install target. A pointer so unclassified
	// disks (inventory captured before the agent computed this) serialize as
	// absent — the UI then falls back to its model/transport heuristic instead of
	// treating a zero-value false as "not eligible" and hiding every disk.
	OSEligible *bool `json:"osEligible,omitempty"`
}

type Card struct {
	Slot string `json:"slot,omitempty"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

type Inventory struct {
	FetchedAt    string `json:"fetchedAt"`
	Source       string `json:"source"` // redfish | ipmi
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	Serial       string `json:"serial,omitempty"`
	CPUModel     string `json:"cpuModel,omitempty"`
	CPUCount     int    `json:"cpuCount,omitempty"`
	CPUCores     int    `json:"cpuCores,omitempty"`
	MemoryBytes  int64  `json:"memoryBytes,omitempty"`
	NICs         []NIC  `json:"nics,omitempty"`
	Disks        []Disk `json:"disks,omitempty"`
	Cards        []Card `json:"cards,omitempty"`
}

// Assignment binds a machine to a cluster node (by hostname). A machine may be
// assigned across multiple clusters (Assignments); within one cluster it should
// map to a single node (a duplicate is surfaced as a non-blocking UI error).
type Assignment struct {
	ClusterID string `json:"clusterId"`
	Hostname  string `json:"hostname"`
	// OSDisk is the chosen install target disk (e.g. a disk name from the
	// fetched inventory); empty until picked in the assignment flow.
	OSDisk string `json:"osDisk,omitempty"`
}

// AssignmentFor returns this machine's assignment in the given cluster, or nil.
func (m Machine) AssignmentFor(clusterID string) *Assignment {
	for i := range m.Assignments {
		if m.Assignments[i].ClusterID == clusterID {
			return &m.Assignments[i]
		}
	}
	return nil
}

type FetchState string

const (
	FetchIdle     FetchState = "idle"
	FetchFetching FetchState = "fetching"
	FetchOK       FetchState = "ok"
	FetchError    FetchState = "error"
)

// Machine is the API-facing view: no password, HasPassword computed.
type Machine struct {
	ID          string       `json:"id"`
	Label       string       `json:"label"`
	BMC         BMC          `json:"bmc"`
	HasPassword bool         `json:"hasPassword"`
	Inventory   *Inventory   `json:"inventory,omitempty"`
	FetchState  FetchState   `json:"fetchState"`
	FetchError  string       `json:"fetchError,omitempty"`
	Assignment  *Assignment  `json:"assignment,omitempty"` // primary (first) — back-compat
	Assignments []Assignment `json:"assignments,omitempty"`
}

// record is the on-disk form; the password is stored encrypted.
type record struct {
	ID          string       `json:"id"`
	Label       string       `json:"label"`
	BMC         BMC          `json:"bmc"`
	PasswordEnc string       `json:"passwordEnc,omitempty"`
	Inventory   *Inventory   `json:"inventory,omitempty"`
	FetchState  FetchState   `json:"fetchState"`
	FetchError  string       `json:"fetchError,omitempty"`
	Assignment  *Assignment  `json:"assignment,omitempty"` // legacy single (migrated)
	Assignments []Assignment `json:"assignments,omitempty"`
}

func (r *record) toMachine() Machine {
	// Migrate a legacy single assignment into the list on read.
	list := r.Assignments
	if len(list) == 0 && r.Assignment != nil {
		list = []Assignment{*r.Assignment}
	}
	var primary *Assignment
	if len(list) > 0 {
		primary = &list[0]
	}
	return Machine{
		ID:          r.ID,
		Label:       r.Label,
		BMC:         r.BMC,
		HasPassword: r.PasswordEnc != "",
		Inventory:   r.Inventory,
		FetchState:  fetchStateOr(r.FetchState),
		FetchError:  r.FetchError,
		Assignment:  primary,
		Assignments: list,
	}
}

func fetchStateOr(s FetchState) FetchState {
	if s == "" {
		return FetchIdle
	}
	return s
}
