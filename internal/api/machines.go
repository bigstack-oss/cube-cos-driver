package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bigstack-oss/cube-cos-driver/internal/discovery"
	"github.com/bigstack-oss/cube-cos-driver/internal/inventory"
	"github.com/bigstack-oss/cube-cos-driver/internal/orchestrator"
)

type machineHandlers struct {
	store      *inventory.Store
	discoverer discovery.Discoverer
	mgr        *orchestrator.Manager // to mark inspect-boots complete (may be nil)
	inflight   sync.Map              // id -> struct{}, guards concurrent fetch
}

type machineInput struct {
	Label string `json:"label"`
	BMC   struct {
		Address  string  `json:"address"`
		Username string  `json:"username"`
		Password *string `json:"password"`
		Cipher   int     `json:"cipher"`
	} `json:"bmc"`
}

func (m *machineHandlers) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/machines", m.list)
	mux.HandleFunc("POST /api/v1/machines", m.create)
	mux.HandleFunc("GET /api/v1/machines/{id}", m.get)
	mux.HandleFunc("PUT /api/v1/machines/{id}", m.update)
	mux.HandleFunc("DELETE /api/v1/machines/{id}", m.delete)
	mux.HandleFunc("POST /api/v1/machines/{id}/fetch", m.fetch)
	mux.HandleFunc("PUT /api/v1/machines/{id}/assignment", m.assign)
	mux.HandleFunc("DELETE /api/v1/machines/{id}/assignment", m.unassign)
	mux.HandleFunc("POST /api/v1/machines/inventory-report", m.inventoryReport)
	mux.HandleFunc("POST /api/v1/machines/import", m.importFile)
	mux.HandleFunc("GET /api/v1/machines/import/template", m.importTemplate)
}

// inventoryReport ingests an inspect-boot hardware report and merges it into the
// matching machine (by serial), populating CPU/mem/disk/NIC without Redfish.
func (m *machineHandlers) inventoryReport(w http.ResponseWriter, r *http.Request) {
	var rep struct {
		Serial       string           `json:"serial"`
		MACs         []string         `json:"macs"`
		Manufacturer string           `json:"manufacturer"`
		Model        string           `json:"model"`
		CPUModel     string           `json:"cpuModel"`
		CPUCount     int              `json:"cpuCount"`
		MemoryBytes  int64            `json:"memoryBytes"`
		NICs         []inventory.NIC  `json:"nics"`
		Disks        []inventory.Disk `json:"disks"`
		Cards        []inventory.Card `json:"cards"`
	}
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	machines, err := m.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	// Match the reporting node to a stored machine by serial (NUL-normalized —
	// IPMI FRU serials carry trailing NULs) or, failing that, by any NIC MAC.
	serial := normSerial(rep.Serial)
	wantMAC := map[string]bool{}
	for _, mc := range rep.MACs {
		if v := strings.ToLower(strings.TrimSpace(mc)); v != "" {
			wantMAC[v] = true
		}
	}
	var targetID string
	for i := range machines {
		inv := machines[i].Inventory
		if inv == nil {
			continue
		}
		if serial != "" && normSerial(inv.Serial) == serial {
			targetID = machines[i].ID
			break
		}
		for _, nic := range inv.NICs {
			if nic.MAC != "" && wantMAC[strings.ToLower(strings.TrimSpace(nic.MAC))] {
				targetID = machines[i].ID
				break
			}
		}
		if targetID != "" {
			break
		}
	}
	if targetID == "" {
		log.Printf("inventory-report: no machine matched serial=%q", rep.Serial)
		writeJSON(w, http.StatusOK, map[string]bool{"matched": false})
		return
	}
	inv := inventory.Inventory{
		FetchedAt:    time.Now().UTC().Format(time.RFC3339),
		Source:       "inspect",
		Manufacturer: rep.Manufacturer,
		Model:        rep.Model,
		Serial:       rep.Serial,
		CPUModel:     rep.CPUModel,
		CPUCount:     rep.CPUCount,
		MemoryBytes:  rep.MemoryBytes,
		NICs:         rep.NICs,
		Disks:        rep.Disks,
		Cards:        rep.Cards,
	}
	if err := m.store.SetInventory(targetID, inv); err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if m.mgr != nil {
		m.mgr.InspectReported(targetID)
	}
	log.Printf("inventory-report: matched %s (serial=%s) nics=%d disks=%d cpu=%dx mem=%dGB",
		targetID, rep.Serial, len(rep.NICs), len(rep.Disks), rep.CPUCount, rep.MemoryBytes/(1<<30))
	writeJSON(w, http.StatusOK, map[string]bool{"matched": true})
}

func (m *machineHandlers) list(w http.ResponseWriter, r *http.Request) {
	machines, err := m.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, machines)
}

// verifyBMC probes the BMC (IPMI FRU + Redfish) with the given credentials,
// returning the discovered inventory. A wrong address or bad credentials fails
// here — the caller rejects the create/update so the store never holds an
// unreachable machine, and the fresh inventory is saved on success.
func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (m *machineHandlers) verifyBMC(ctx context.Context, address, username, password string) (inventory.Inventory, error) {
	ctx, cancel := context.WithTimeout(ctx, 240*time.Second)
	defer cancel()
	return m.discoverer.Discover(ctx, discovery.Target{
		Address: address, Username: username, Password: password,
	})
}

func (m *machineHandlers) create(w http.ResponseWriter, r *http.Request) {
	var in machineInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if in.Label == "" || in.BMC.Address == "" {
		writeError(w, http.StatusBadRequest, "label and bmc address are required")
		return
	}
	inv, derr := m.verifyBMC(r.Context(), in.BMC.Address, in.BMC.Username, strOrEmpty(in.BMC.Password))
	if derr != nil {
		writeError(w, http.StatusBadGateway, "BMC verification failed — check the address and credentials: %v", derr)
		return
	}
	machine, err := m.store.Create(inventory.Input{
		Label:    in.Label,
		Address:  in.BMC.Address,
		Username: in.BMC.Username,
		Cipher:   in.BMC.Cipher,
		Password: in.BMC.Password,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	_ = m.store.SetInventory(machine.ID, inv)
	if got, gerr := m.store.Get(machine.ID); gerr == nil {
		machine = got
	}
	writeJSON(w, http.StatusCreated, machine)
}

func (m *machineHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	machine, err := m.store.Get(id)
	if errors.Is(err, inventory.ErrNotFound) {
		writeError(w, http.StatusNotFound, "machine %s not found", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, machine)
}

func (m *machineHandlers) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	var in machineInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	// A blank password on edit means "keep the stored one" — verify with it.
	password := strOrEmpty(in.BMC.Password)
	if password == "" {
		if _, _, sp, cerr := m.store.Credentials(id); cerr == nil {
			password = sp
		}
	}
	inv, derr := m.verifyBMC(r.Context(), in.BMC.Address, in.BMC.Username, password)
	if derr != nil {
		writeError(w, http.StatusBadGateway, "BMC verification failed — check the address and credentials: %v", derr)
		return
	}
	machine, err := m.store.Update(id, inventory.Input{
		Label:    in.Label,
		Address:  in.BMC.Address,
		Username: in.BMC.Username,
		Cipher:   in.BMC.Cipher,
		Password: in.BMC.Password, // nil = keep stored
	})
	if errors.Is(err, inventory.ErrNotFound) {
		writeError(w, http.StatusNotFound, "machine %s not found", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	_ = m.store.SetInventory(id, inv)
	if got, gerr := m.store.Get(id); gerr == nil {
		machine = got
	}
	writeJSON(w, http.StatusOK, machine)
}

func (m *machineHandlers) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	err := m.store.Delete(id)
	if errors.Is(err, inventory.ErrNotFound) {
		writeError(w, http.StatusNotFound, "machine %s not found", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fetch triggers async hardware discovery. Returns 202; progress is visible
// via the machine's fetchState on GET.
func (m *machineHandlers) fetch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	address, username, password, err := m.store.Credentials(id)
	if errors.Is(err, inventory.ErrNotFound) {
		writeError(w, http.StatusNotFound, "machine %s not found", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}

	// One fetch per machine at a time.
	if _, running := m.inflight.LoadOrStore(id, struct{}{}); running {
		writeJSON(w, http.StatusAccepted, map[string]string{"message": "fetch already in progress"})
		return
	}
	if err := m.store.SetFetchState(id, inventory.FetchFetching, ""); err != nil {
		m.inflight.Delete(id)
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}

	target := discovery.Target{Address: address, Username: username, Password: password}
	go m.runFetch(id, target)

	writeJSON(w, http.StatusAccepted, map[string]string{"message": "fetch started"})
}

func (m *machineHandlers) assign(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		ClusterID string `json:"clusterId"`
		Hostname  string `json:"hostname"`
		OSDisk    string `json:"osDisk"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if body.ClusterID == "" || body.Hostname == "" {
		writeError(w, http.StatusBadRequest, "clusterId and hostname are required")
		return
	}
	machine, err := m.store.Assign(id, body.ClusterID, body.Hostname, body.OSDisk)
	if errors.Is(err, inventory.ErrNotFound) {
		writeError(w, http.StatusNotFound, "machine %s not found", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, machine)
}

func (m *machineHandlers) unassign(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	machine, err := m.store.Unassign(id)
	if errors.Is(err, inventory.ErrNotFound) {
		writeError(w, http.StatusNotFound, "machine %s not found", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, machine)
}

// importFile accepts a multipart "file" (.xlsx or .csv) and bulk-creates
// machines. Returns {created, errors:[{row,message}]}.
func (m *machineHandlers) importFile(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload: %v", err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	name := strings.ToLower(header.Filename)
	var res inventory.ImportResult
	switch {
	case strings.HasSuffix(name, ".xlsx"):
		res, err = inventory.ParseXLSX(file)
	case strings.HasSuffix(name, ".csv"):
		res, err = inventory.ParseCSV(file)
	default:
		writeError(w, http.StatusBadRequest, "unsupported file type (use .xlsx or .csv)")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}

	created, errs := m.store.ImportMany(res)
	writeJSON(w, http.StatusOK, map[string]any{
		"created": created,
		"errors":  errs,
	})
}

func (m *machineHandlers) importTemplate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="machines-template.csv"`)
	w.Write([]byte(inventory.CSVTemplate))
}

func (m *machineHandlers) runFetch(id string, target discovery.Target) {
	defer m.inflight.Delete(id)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	inv, err := m.discoverer.Discover(ctx, target)
	if err != nil {
		_ = m.store.SetFetchState(id, inventory.FetchError, err.Error())
		return
	}
	_ = m.store.SetInventory(id, inv)
}
