package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bigstack-oss/cube-cos-snapshot/internal/agent"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/generator"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/inventory"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/orchestrator"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/storage"
)

type deployHandlers struct {
	clusters *storage.Store
	machines *inventory.Store
	mgr      *orchestrator.Manager
}

func (h *deployHandlers) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/clusters/{id}/deploy/plan", h.plan)
	mux.HandleFunc("POST /api/v1/clusters/{id}/deploy", h.start)
	mux.HandleFunc("GET /api/v1/clusters/{id}/deploy", h.status)
	mux.HandleFunc("POST /api/v1/clusters/{id}/deploy/cancel", h.cancel)
	mux.HandleFunc("POST /api/v1/agents/checkin", h.checkin)
	mux.HandleFunc("POST /api/v1/agents/report", h.report)
	mux.HandleFunc("POST /api/v1/agents/preflight/checkin", h.preflightCheckin)
	mux.HandleFunc("POST /api/v1/agents/preflight/report", h.preflightReport)
	mux.HandleFunc("POST /api/v1/agents/preflight/greenlight", h.greenlight)
}

// matchByMAC finds the assigned machine whose discovered NICs include any of
// the given MACs (case-insensitive).
func (h *deployHandlers) matchByMAC(macs []string) (*inventory.Machine, error) {
	want := map[string]bool{}
	for _, m := range macs {
		want[strings.ToLower(strings.TrimSpace(m))] = true
	}
	machines, err := h.machines.List()
	if err != nil {
		return nil, err
	}
	for i := range machines {
		if machines[i].Assignment == nil {
			continue
		}
		for _, mac := range macsOf(machines[i]) {
			if want[strings.ToLower(mac)] {
				return &machines[i], nil
			}
		}
	}
	return nil, nil
}

type planRow struct {
	Hostname     string   `json:"hostname"`
	Assigned     bool     `json:"assigned"`
	MachineLabel string   `json:"machineLabel,omitempty"`
	BMCAddress   string   `json:"bmcAddress,omitempty"`
	OSDisk       string   `json:"osDisk,omitempty"`
	MACs         []string `json:"macs,omitempty"`
}

func macsOf(m inventory.Machine) []string {
	var out []string
	if m.Inventory != nil {
		for _, n := range m.Inventory.NICs {
			if n.MAC != "" {
				out = append(out, n.MAC)
			}
		}
	}
	return out
}

// buildNodes resolves a cluster's deployable nodes, a per-node plan, and the
// master hostname (first control-function node, whose FTS gates the rest).
func (h *deployHandlers) buildNodes(id string) (nodes []orchestrator.Node, rows []planRow, allAssigned bool, master string, err error) {
	detail, err := h.clusters.Detail(id)
	if err != nil {
		return nil, nil, false, "", err
	}
	if ctl := generator.GetControlInfo(detail.NodeData); len(ctl.Hostnames) > 0 {
		master = ctl.Hostnames[0]
	}
	machines, err := h.machines.List()
	if err != nil {
		return nil, nil, false, "", err
	}
	byHost := map[string]inventory.Machine{}
	for _, m := range machines {
		if m.Assignment != nil && m.Assignment.ClusterID == id {
			byHost[m.Assignment.Hostname] = m
		}
	}
	allAssigned = true
	for _, node := range detail.NodeData {
		m, ok := byHost[node.Hostname]
		row := planRow{Hostname: node.Hostname, Assigned: ok}
		if !ok {
			allAssigned = false
			rows = append(rows, row)
			continue
		}
		addr, user, pass, cerr := h.machines.Credentials(m.ID)
		if cerr != nil {
			return nil, nil, false, "", cerr
		}
		macs := macsOf(m)
		row.MachineLabel = m.Label
		row.BMCAddress = addr
		row.OSDisk = m.Assignment.OSDisk
		row.MACs = macs
		rows = append(rows, row)
		nodes = append(nodes, orchestrator.Node{
			Hostname:   node.Hostname,
			MachineID:  m.ID,
			BMCAddress: addr,
			BMCUser:    user,
			BMCPass:    pass,
			MACs:       macs,
			OSDisk:     m.Assignment.OSDisk,
		})
	}
	return nodes, rows, allAssigned, master, nil
}

func (h *deployHandlers) plan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	_, rows, allAssigned, _, err := h.buildNodes(id)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "cluster %s not found", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	status := http.StatusOK
	if !allAssigned {
		status = http.StatusConflict // not all nodes assigned
	}
	writeJSON(w, status, map[string]any{"allAssigned": allAssigned, "nodes": rows})
}

func (h *deployHandlers) start(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Confirm   bool     `json:"confirm"`
		Hostnames []string `json:"hostnames"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if !body.Confirm {
		writeError(w, http.StatusBadRequest, "deploy requires confirm=true")
		return
	}
	nodes, _, allAssigned, master, err := h.buildNodes(id)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "cluster %s not found", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if !allAssigned {
		writeError(w, http.StatusConflict, "every node must be assigned a server before deploy")
		return
	}
	if len(body.Hostnames) > 0 {
		want := map[string]bool{}
		for _, hn := range body.Hostnames {
			want[hn] = true
		}
		filtered := nodes[:0]
		for _, n := range nodes {
			if want[n.Hostname] {
				filtered = append(filtered, n)
			}
		}
		nodes = filtered
	}
	if len(nodes) == 0 {
		writeError(w, http.StatusBadRequest, "no matching nodes to deploy")
		return
	}
	dep, err := h.mgr.Start(id, nodes, master, h.verifyTargets(id))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusAccepted, dep)
}

func (h *deployHandlers) status(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	dep, err := h.mgr.Status(id)
	if errors.Is(err, orchestrator.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no deploy for cluster %s", id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, dep)
}

func (h *deployHandlers) cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	h.mgr.Cancel(id)
	writeJSON(w, http.StatusOK, map[string]string{"message": "cancelled"})
}

// checkin matches an agent's MACs to an appointment and returns its snapshot
// URL + preflight targets, advancing that node to checked-in.
func (h *deployHandlers) checkin(w http.ResponseWriter, r *http.Request) {
	var req agent.CheckinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	matched, err := h.matchByMAC(req.MACs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if matched == nil {
		writeJSON(w, http.StatusOK, agent.CheckinResponse{Appointed: false})
		return
	}

	cid := matched.Assignment.ClusterID
	host := matched.Assignment.Hostname
	scheme := "http://"
	if r.TLS != nil {
		scheme = "https://"
	}
	serverHost := r.Host
	if i := strings.LastIndex(serverHost, ":"); i > 0 {
		serverHost = serverHost[:i]
	}
	// Master-first gating: a non-master node holds until the master's FTS is
	// done. The agent re-checks in while holding.
	hold := h.mgr.CheckIn(cid, host)
	resp := agent.CheckinResponse{
		Appointed:     true,
		Hold:          hold,
		ClusterID:     cid,
		Hostname:      host,
		SnapshotURL:   scheme + r.Host + "/api/v1/clusters/" + cid + "/nodes/" + host + "/download",
		ServerTimeUTC: time.Now().UTC().Format(time.RFC3339),
		Preflight:     h.preflightTargets(cid, host, serverHost),
	}
	writeJSON(w, http.StatusOK, resp)
}

// verifyTargets are the Tier-2 whole-cluster reachability targets (control
// node mgmt IPs), tested from the server only once every node reaches done.
func (h *deployHandlers) verifyTargets(clusterID string) []string {
	detail, err := h.clusters.Detail(clusterID)
	if err != nil {
		return nil
	}
	var targets []string
	for _, ip := range generator.GetControlInfo(detail.NodeData).IPs {
		if ip != "" && ip != "None" {
			targets = append(targets, ip)
		}
	}
	return targets
}

// preflightTargets derives connectivity targets for a node from its cluster
// config: default gateway, DNS, the snapshot server, and control peers.
func (h *deployHandlers) preflightTargets(clusterID, hostname, serverHost string) agent.Preflight {
	pf := agent.Preflight{Server: serverHost}
	detail, err := h.clusters.Detail(clusterID)
	if err != nil {
		return pf
	}
	pf.DNS = detail.ClusterConfig.DNS
	var selfMgmtIP string
	for _, n := range detail.NodeData {
		if n.Hostname == hostname {
			pf.Gateway = n.DefaultGateway
			for _, f := range n.AllIFs() {
				if f.ID == n.RoleSettings.MgmtIF.ID {
					selfMgmtIP = f.IPAddr
				}
			}
		}
	}
	ctl := generator.GetControlInfo(detail.NodeData)
	for _, ip := range ctl.IPs {
		if ip != "" && ip != selfMgmtIP {
			pf.Peers = append(pf.Peers, ip)
		}
	}
	return pf
}

func (h *deployHandlers) report(w http.ResponseWriter, r *http.Request) {
	var req agent.ReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if req.ClusterID == "" || req.Hostname == "" {
		writeError(w, http.StatusBadRequest, "clusterId and hostname required")
		return
	}
	var pf []orchestrator.PreflightResult
	for _, p := range req.Preflight {
		pf = append(pf, orchestrator.PreflightResult{Target: p.Target, OK: p.OK, Detail: p.Detail})
	}
	h.mgr.Report(req.ClusterID, req.Hostname, orchestrator.State(req.State), req.Message, pf)
	writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}

// preflightCheckin (installer phase): match the node by MAC and return its
// topology+peer bundle and the server clock. Marks the node preflighting.
func (h *deployHandlers) preflightCheckin(w http.ResponseWriter, r *http.Request) {
	var req agent.CheckinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	matched, err := h.matchByMAC(req.MACs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if matched == nil {
		writeJSON(w, http.StatusOK, agent.PreflightCheckinResponse{Appointed: false})
		return
	}
	cid := matched.Assignment.ClusterID
	host := matched.Assignment.Hostname
	detail, err := h.clusters.Detail(cid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	bundle := generator.BuildPreflightBundles(detail)[host]
	h.mgr.PreflightProgress(cid, host)
	writeJSON(w, http.StatusOK, agent.PreflightCheckinResponse{
		Appointed:     true,
		ClusterID:     cid,
		Hostname:      host,
		ServerTimeUTC: time.Now().UTC().Format(time.RFC3339),
		Bundle:        bundle,
	})
}

// preflightReport (installer phase): record a node's carrier/skew/ping-matrix
// result.
func (h *deployHandlers) preflightReport(w http.ResponseWriter, r *http.Request) {
	var req agent.PreflightReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if req.ClusterID == "" || req.Hostname == "" {
		writeError(w, http.StatusBadRequest, "clusterId and hostname required")
		return
	}
	var matrix []orchestrator.PreflightResult
	for _, p := range req.Matrix {
		matrix = append(matrix, orchestrator.PreflightResult{Target: p.Target, OK: p.OK, Detail: p.Detail})
	}
	h.mgr.PreflightReport(req.ClusterID, req.Hostname, orchestrator.NodePreflight{
		CarrierOK:    req.CarrierOK,
		ClockSkewSec: req.ClockSkewSec,
		Matrix:       matrix,
		Passed:       req.Passed,
	})
	writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}

// greenlight (installer phase): report whether green light 1 has cleared so the
// node may restore.
func (h *deployHandlers) greenlight(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClusterID string `json:"clusterId"`
		Hostname  string `json:"hostname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if req.ClusterID == "" || req.Hostname == "" {
		writeError(w, http.StatusBadRequest, "clusterId and hostname required")
		return
	}
	writeJSON(w, http.StatusOK, agent.GreenlightResponse{Clear: h.mgr.GreenLight1(req.ClusterID, req.Hostname)})
}
