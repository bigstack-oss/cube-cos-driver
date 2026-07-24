package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/bigstack-oss/cube-cos-snapshot/internal/discovery"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/inventory"
)

type machineHandlers struct {
	store      *inventory.Store
	discoverer discovery.Discoverer
	inflight   sync.Map // id -> struct{}, guards concurrent fetch
}

type machineInput struct {
	Label string `json:"label"`
	BMC   struct {
		Address  string  `json:"address"`
		Username string  `json:"username"`
		Password *string `json:"password"`
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
}

func (m *machineHandlers) list(w http.ResponseWriter, r *http.Request) {
	machines, err := m.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, machines)
}

func (m *machineHandlers) create(w http.ResponseWriter, r *http.Request) {
	var in machineInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	machine, err := m.store.Create(inventory.Input{
		Label:    in.Label,
		Address:  in.BMC.Address,
		Username: in.BMC.Username,
		Password: in.BMC.Password,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
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
	machine, err := m.store.Update(id, inventory.Input{
		Label:    in.Label,
		Address:  in.BMC.Address,
		Username: in.BMC.Username,
		Password: in.BMC.Password,
	})
	if errors.Is(err, inventory.ErrNotFound) {
		writeError(w, http.StatusNotFound, "machine %s not found", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
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
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if body.ClusterID == "" || body.Hostname == "" {
		writeError(w, http.StatusBadRequest, "clusterId and hostname are required")
		return
	}
	machine, err := m.store.Assign(id, body.ClusterID, body.Hostname)
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

func (m *machineHandlers) runFetch(id string, target discovery.Target) {
	defer m.inflight.Delete(id)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	inv, err := m.discoverer.Discover(ctx, target)
	if err != nil {
		_ = m.store.SetFetchState(id, inventory.FetchError, err.Error())
		return
	}
	_ = m.store.SetInventory(id, inv)
}
