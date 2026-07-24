// Package inventory defines the machine (BMC) records and their fetched
// hardware facts, and persists them with encrypted credentials.
package inventory

type BMC struct {
	Address  string `json:"address"`
	Username string `json:"username"`
}

type NIC struct {
	Name      string `json:"name,omitempty"`
	MAC       string `json:"mac,omitempty"`
	SpeedMbps int    `json:"speedMbps,omitempty"`
	Up        bool   `json:"up,omitempty"`
}

type Disk struct {
	Name      string `json:"name,omitempty"`
	Model     string `json:"model,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	Type      string `json:"type,omitempty"` // HDD | SSD | NVMe
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

// Assignment binds a machine to a cluster node (by hostname). One machine
// maps to at most one node, and one node to at most one machine.
type Assignment struct {
	ClusterID string `json:"clusterId"`
	Hostname  string `json:"hostname"`
	// OSDisk is the chosen install target disk (e.g. a disk name from the
	// fetched inventory); empty until picked in the assignment flow.
	OSDisk string `json:"osDisk,omitempty"`
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
	ID          string      `json:"id"`
	Label       string      `json:"label"`
	BMC         BMC         `json:"bmc"`
	HasPassword bool        `json:"hasPassword"`
	Inventory   *Inventory  `json:"inventory,omitempty"`
	FetchState  FetchState  `json:"fetchState"`
	FetchError  string      `json:"fetchError,omitempty"`
	Assignment  *Assignment `json:"assignment,omitempty"`
}

// record is the on-disk form; the password is stored encrypted.
type record struct {
	ID          string      `json:"id"`
	Label       string      `json:"label"`
	BMC         BMC         `json:"bmc"`
	PasswordEnc string      `json:"passwordEnc,omitempty"`
	Inventory   *Inventory  `json:"inventory,omitempty"`
	FetchState  FetchState  `json:"fetchState"`
	FetchError  string      `json:"fetchError,omitempty"`
	Assignment  *Assignment `json:"assignment,omitempty"`
}

func (r *record) toMachine() Machine {
	return Machine{
		ID:          r.ID,
		Label:       r.Label,
		BMC:         r.BMC,
		HasPassword: r.PasswordEnc != "",
		Inventory:   r.Inventory,
		FetchState:  fetchStateOr(r.FetchState),
		FetchError:  r.FetchError,
		Assignment:  r.Assignment,
	}
}

func fetchStateOr(s FetchState) FetchState {
	if s == "" {
		return FetchIdle
	}
	return s
}
