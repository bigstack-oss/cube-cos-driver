package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bigstack-oss/cube-cos-driver/internal/enterprise"
	"github.com/bigstack-oss/cube-cos-driver/internal/model"
	"github.com/bigstack-oss/cube-cos-driver/internal/secret"
	"github.com/bigstack-oss/cube-cos-driver/internal/storage"
)

type enterpriseHandlers struct {
	clusters *storage.Store
	mgr      *enterprise.Manager
	dataDir  string
	box      *secret.Box
}

func (h *enterpriseHandlers) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/enterprise/artifacts", h.artifacts)
	mux.HandleFunc("GET /api/v1/enterprise/installs", h.installs)
	mux.HandleFunc("POST /api/v1/clusters/{id}/enterprise/cluster-info", h.clusterInfo)
	mux.HandleFunc("POST /api/v1/clusters/{id}/enterprise/install", h.start)
	mux.HandleFunc("GET /api/v1/clusters/{id}/enterprise/install", h.status)
	mux.HandleFunc("POST /api/v1/clusters/{id}/enterprise/install/step/next", h.next)
	mux.HandleFunc("POST /api/v1/clusters/{id}/enterprise/install/cancel", h.cancel)
}

// installs lists every known install run (across all clusters) for the dashboard.
func (h *enterpriseHandlers) installs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.mgr.List())
}

// artifacts lists pre-staged install artifacts discovered under dataDir.
func (h *enterpriseHandlers) artifacts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, enterprise.DiscoverArtifacts(h.dataDir))
}

// clusterInfo live-queries the selected cluster's OpenStack for its projects
// and networks, to populate the install form's project/net fields.
func (h *enterpriseHandlers) clusterInfo(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // password optional; default when blank
	detail, err := h.clusters.Detail(id)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "cluster %s not found", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	host := connectHost(detail)
	if host == "" {
		writeError(w, http.StatusBadRequest, "cluster has no reachable VIP or management IP")
		return
	}
	password := body.Password
	if password == "" {
		password = defaultPassword(host)
	}
	q, err := h.mgr.Introspect(host, password)
	if err != nil {
		writeError(w, http.StatusBadGateway, "cluster query failed: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Projects         []string `json:"projects"`
		Networks         []string `json:"networks"`
		AirgapSupported  bool     `json:"airgapSupported"`
		SuggestedLBIP    string   `json:"suggestedLBIP"`
		SuggestedStorage string   `json:"suggestedStorage"`
		Version          string   `json:"version"`
		Manifest         string   `json:"manifest"`
		Manifests        []string `json:"manifests"`
	}{q.Projects, q.Networks, q.AirgapSupported, q.SuggestedLBIP, q.SuggestedStorage, q.Version, q.Manifest, q.Manifests})
}

// defaultPassword derives the cluster root password from the connect host's
// last two octets (e.g. 10.32.10.140 -> Cube@10.140), matching the CubeCOS FTS
// default. For HA that host is the VIP; for a single node it's its mgmt IP.
func defaultPassword(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) < 4 {
		return "Cube@" + host
	}
	return "Cube@" + strings.Join(parts[2:], ".")
}

// connectHost is the IP the driver reaches the cluster on (and derives the
// default password from): the VIP for an HA cluster, else the single node's
// management-interface IP.
func connectHost(d model.ClusterDetail) string {
	if vip := d.ClusterConfig.HASettings.VirtualIP; vip != "" {
		return vip
	}
	for _, n := range d.NodeData {
		for _, f := range n.AllIFs() {
			if f.ID == n.RoleSettings.MgmtIF.ID && f.IPAddr != "" {
				return f.IPAddr
			}
		}
	}
	return ""
}

// persistPassword encrypts the install password at rest as a sidecar file
// (mirrors the inventory store's box.Encrypt idiom for BMC passwords).
// Best-effort: Start already has the plaintext it needs to dial.
func (h *enterpriseHandlers) persistPassword(id, module, password string) {
	enc, err := h.box.Encrypt(password)
	if err != nil {
		log.Printf("enterprise: encrypt password for %s/%s: %v", id, module, err)
		return
	}
	p := filepath.Join(h.dataDir, "installs", id+"-"+module+".pw")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		log.Printf("enterprise: persist password for %s/%s: %v", id, module, err)
		return
	}
	if err := os.WriteFile(p, []byte(enc), 0o600); err != nil {
		log.Printf("enterprise: persist password for %s/%s: %v", id, module, err)
	}
}

// start begins (or resumes, if manual) an enterprise module install against
// the cluster's VIP.
func (h *enterpriseHandlers) start(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Module         string                   `json:"module"`
		Params         enterprise.InstallParams `json:"params"`
		Manual         bool                     `json:"manual"`
		SimulateAirgap bool                     `json:"simulateAirgap"`
		Password       string                   `json:"password"`
		Manifest       string                   `json:"manifest"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	// BuildPlan silently degrades an unknown module to a preflight-only plan,
	// so validate here — it's the only checkpoint before mgr.Start.
	if body.Module != enterprise.ModuleAppFW && body.Module != enterprise.ModuleCMP {
		writeError(w, http.StatusBadRequest, "unknown module %q", body.Module)
		return
	}
	detail, err := h.clusters.Detail(id)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "cluster %s not found", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	host := connectHost(detail)
	if host == "" {
		writeError(w, http.StatusBadRequest, "cluster has no reachable VIP or management IP")
		return
	}
	password := body.Password
	if password == "" {
		password = defaultPassword(host)
	}
	manifest := enterprise.FindManifest(enterprise.LoadManifests(h.dataDir), body.Manifest)
	in, err := h.mgr.Start(id, body.Module, host, password, body.Params, body.Manual, body.SimulateAirgap, manifest)
	if err != nil {
		writeError(w, http.StatusConflict, "%v", err)
		return
	}
	// Only persist the password sidecar once Start actually reserved the run —
	// a failed Start (e.g. duplicate in-flight) must not leave a stray file.
	h.persistPassword(id, body.Module, password)
	writeJSON(w, http.StatusAccepted, in)
}

// status returns the current install record for a cluster+module.
func (h *enterpriseHandlers) status(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	module := r.URL.Query().Get("module")
	in, ok := h.mgr.Status(id, module)
	if !ok {
		writeError(w, http.StatusNotFound, "no install for %s/%s", id, module)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// next advances a manual install by one step (operator "Next").
func (h *enterpriseHandlers) next(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	module := r.URL.Query().Get("module")
	if err := h.mgr.Next(id, module); err != nil {
		writeError(w, http.StatusConflict, "%v", err)
		return
	}
	in, _ := h.mgr.Status(id, module)
	writeJSON(w, http.StatusOK, in)
}

// cancel stops an in-flight install.
func (h *enterpriseHandlers) cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	module := r.URL.Query().Get("module")
	h.mgr.Cancel(id, module)
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "cancelled"})
}
