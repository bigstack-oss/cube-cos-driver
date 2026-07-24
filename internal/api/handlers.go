package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/bigstack-oss/cube-cos-snapshot/internal/inventory"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/model"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/storage"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type handlers struct {
	store *storage.Store
	// machines, when set, has its node bindings cleared when a cluster is
	// deleted.
	machines *inventory.Store
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, map[string]string{"message": fmt.Sprintf(format, args...)})
}

// pathValue extracts and validates a path parameter against namePattern
// (also a path-traversal guard: no "/", no "..").
func pathValue(w http.ResponseWriter, r *http.Request, key string) (string, bool) {
	v := r.PathValue(key)
	if !namePattern.MatchString(v) || v == "." || v == ".." {
		writeError(w, http.StatusBadRequest, "invalid %s", key)
		return "", false
	}
	return v, true
}

func (h *handlers) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/clusters", h.list)
	mux.HandleFunc("GET /api/v1/clusters/{id}", h.detail)
	mux.HandleFunc("PUT /api/v1/clusters/{id}", h.save)
	mux.HandleFunc("DELETE /api/v1/clusters/{id}", h.delete)
	mux.HandleFunc("GET /api/v1/clusters/{id}/download", h.downloadZip)
	mux.HandleFunc("GET /api/v1/clusters/{id}/nodes/{hostname}/download", h.downloadSnapshot)
	// Unknown API paths must not fall through to the SPA handler.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint")
	})
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	digests, err := h.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, digests)
}

func (h *handlers) detail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	d, err := h.store.Detail(id)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "cluster %s not found", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "detail failed: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *handlers) save(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	var d model.ClusterDetail
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		writeError(w, http.StatusBadRequest, "invalid clusterDetail JSON: %v", err)
		return
	}
	if d.ShortID() != id {
		writeError(w, http.StatusBadRequest, "cluster id %s does not match body id %s", id, d.ShortID())
		return
	}
	if err := d.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if err := h.store.Save(d); err != nil {
		writeError(w, http.StatusInternalServerError, "save failed: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "saved"})
}

func (h *handlers) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	err := h.store.Delete(id)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "cluster %s not found", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed: %v", err)
		return
	}
	if h.machines != nil {
		// Best-effort: release any BMC machines bound to this cluster.
		_ = h.machines.UnassignCluster(id)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) downloadZip(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	path, name, err := h.store.ClusterZipPath(id)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "cluster %s not found", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "download failed: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	http.ServeFile(w, r, path)
}

func (h *handlers) downloadSnapshot(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	hostname, ok := pathValue(w, r, "hostname")
	if !ok {
		return
	}
	path, err := h.store.SnapshotPath(id, hostname)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "snapshot %s/%s not found", id, hostname)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "download failed: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", hostname+".snapshot"))
	http.ServeFile(w, r, path)
}
