package api

import (
	"encoding/json"
	"errors"
	"fmt"
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
	dataDir  string           // runtime state (installs, pw sidecars)
	dir      *enterprise.Dir  // enterprise images folder (appfw+cmp artifacts)
	box      *secret.Box
}

func (h *enterpriseHandlers) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/enterprise/artifacts", h.artifacts)
	mux.HandleFunc("GET /api/v1/enterprise/dir", h.getDir)
	mux.HandleFunc("PUT /api/v1/enterprise/dir", h.setDir)
	mux.HandleFunc("GET /api/v1/enterprise/installs", h.installs)
	mux.HandleFunc("GET /api/v1/enterprise/step-stats", h.stepStats)
	mux.HandleFunc("POST /api/v1/clusters/{id}/enterprise/cluster-info", h.clusterInfo)
	mux.HandleFunc("POST /api/v1/clusters/{id}/enterprise/install", h.start)
	mux.HandleFunc("POST /api/v1/clusters/{id}/enterprise/uninstall", h.uninstall)
	mux.HandleFunc("GET /api/v1/clusters/{id}/enterprise/install", h.status)
	mux.HandleFunc("POST /api/v1/clusters/{id}/enterprise/install/step/next", h.next)
	mux.HandleFunc("POST /api/v1/clusters/{id}/enterprise/install/cancel", h.cancel)
}

// installs lists every known install run (across all clusters) for the dashboard.
func (h *enterpriseHandlers) installs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.mgr.List())
}

// stepStats returns the median observed duration (seconds) per step name across
// past runs — the data-driven "typical" the progress view shows next to elapsed.
func (h *enterpriseHandlers) stepStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"stepDurations": h.mgr.StepDurations()})
}

// artifacts lists pre-staged install artifacts under the enterprise images folder.
func (h *enterpriseHandlers) artifacts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, enterprise.DiscoverArtifacts(h.dir.Get()))
}

// getDir returns the enterprise images folder + whether it's mounted and how
// many appfw/cmp/advisor artifacts it holds. The UI settings modal reads this.
func (h *enterpriseHandlers) getDir(w http.ResponseWriter, r *http.Request) {
	dir, mounted, appfw, cmp, advisor := h.dir.Status()
	writeJSON(w, http.StatusOK, map[string]any{"imageDir": dir, "mounted": mounted, "appfwCount": appfw, "cmpCount": cmp, "advisorCount": advisor})
}

// setDir points the driver at a new enterprise images folder (e.g. a mounted
// USB / virtual media). Validated (must exist), persisted, and the portal
// scripts are re-materialized there. Returns the refreshed status.
func (h *enterpriseHandlers) setDir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ImageDir string `json:"imageDir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if err := h.dir.Set(strings.TrimSpace(body.ImageDir)); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	dir, mounted, appfw, cmp, advisor := h.dir.Status()
	writeJSON(w, http.StatusOK, map[string]any{"imageDir": dir, "mounted": mounted, "appfwCount": appfw, "cmpCount": cmp, "advisorCount": advisor})
}

// clusterInfo live-queries the selected cluster's OpenStack for its projects
// and networks, to populate the install form's project/net fields.
func (h *enterpriseHandlers) clusterInfo(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Password  string `json:"password"`
		Vip       string `json:"vip"`       // ad-hoc target by VIP instead of a configured cluster
		Framework string `json:"framework"` // target framework, to suggest its ingress IP if it exists
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // password optional; default when blank
	host, code, msg := h.resolveHost(id, body.Vip)
	if code != 0 {
		writeError(w, code, "%s", msg)
		return
	}
	password := body.Password
	if password == "" {
		password = defaultPassword(host)
	}
	q, err := h.mgr.Introspect(host, password, body.Framework)
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

// resolveHost picks the SSH host for an enterprise op: an explicit vip from the
// request (an ad-hoc target not in the store) wins; otherwise the configured
// cluster's connect IP. Returns (host, httpCode, message); code == 0 on success.
func (h *enterpriseHandlers) resolveHost(id, vip string) (string, int, string) {
	if v := strings.TrimSpace(vip); v != "" {
		return v, 0, ""
	}
	detail, err := h.clusters.Detail(id)
	if errors.Is(err, storage.ErrNotFound) {
		return "", http.StatusNotFound, fmt.Sprintf("cluster %s not found", id)
	}
	if err != nil {
		return "", http.StatusInternalServerError, err.Error()
	}
	host := connectHost(detail)
	if host == "" {
		return "", http.StatusBadRequest, "cluster has no reachable VIP or management IP"
	}
	return host, 0, ""
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
		Vip            string                   `json:"vip"` // ad-hoc target by VIP
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	// BuildPlan silently degrades an unknown module to a preflight-only plan,
	// so validate here — it's the only checkpoint before mgr.Start.
	switch body.Module {
	case enterprise.ModuleAppFW, enterprise.ModuleCMP, enterprise.ModuleAdvisor:
	default:
		writeError(w, http.StatusBadRequest, "unknown module %q", body.Module)
		return
	}
	// Both modules' plans import the rancher image + framework_create keyed on
	// OSImage; an empty value can't be resolved to a file and would scp the
	// artifacts directory. Reject it here with a clear message.
	if body.Params.OSImage == "" {
		writeError(w, http.StatusBadRequest, "params.OSImage is required (the rancher cluster image .raw)")
		return
	}
	host, code, msg := h.resolveHost(id, body.Vip)
	if code != 0 {
		writeError(w, code, "%s", msg)
		return
	}
	password := body.Password
	if password == "" {
		password = defaultPassword(host)
	}
	manifest := enterprise.FindManifest(enterprise.LoadManifests(h.dir.Get()), body.Manifest)
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

// uninstall tears a module down — CMP: helm-uninstall the portal; App-Framework:
// framework_delete (removes the framework and every app on it). Shares the
// install run machinery (same status/progress/cancel endpoints).
func (h *enterpriseHandlers) uninstall(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Module   string                   `json:"module"`
		Params   enterprise.InstallParams `json:"params"`
		Manual   bool                     `json:"manual"`
		Password string                   `json:"password"`
		Vip      string                   `json:"vip"` // ad-hoc target by VIP
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	switch body.Module {
	case enterprise.ModuleAppFW, enterprise.ModuleCMP, enterprise.ModuleAdvisor:
	default:
		writeError(w, http.StatusBadRequest, "unknown module %q", body.Module)
		return
	}
	// Uninstall needs a framework name, not the image params; default to "appfw".
	if body.Params.Project == "" {
		body.Params.Project = "appfw"
	}
	if body.Params.Framework == "" {
		body.Params.Framework = body.Params.Project
	}
	host, code, msg := h.resolveHost(id, body.Vip)
	if code != 0 {
		writeError(w, code, "%s", msg)
		return
	}
	password := body.Password
	if password == "" {
		password = defaultPassword(host)
	}
	in, err := h.mgr.StartUninstall(id, body.Module, host, password, body.Params, body.Manual)
	if err != nil {
		writeError(w, http.StatusConflict, "%v", err)
		return
	}
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
