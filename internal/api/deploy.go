package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/bigstack-oss/cube-cos-driver/internal/agent"
	"github.com/bigstack-oss/cube-cos-driver/internal/generator"
	"github.com/bigstack-oss/cube-cos-driver/internal/inventory"
	"github.com/bigstack-oss/cube-cos-driver/internal/orchestrator"
	"github.com/bigstack-oss/cube-cos-driver/internal/storage"
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
	mux.HandleFunc("POST /api/v1/clusters/{id}/deploy/release/{hostname}", h.release)
	mux.HandleFunc("POST /api/v1/agents/checkin", h.checkin)
	mux.HandleFunc("POST /api/v1/agents/report", h.report)
	mux.HandleFunc("POST /api/v1/agents/preflight/checkin", h.preflightCheckin)
	mux.HandleFunc("POST /api/v1/agents/preflight/report", h.preflightReport)
	mux.HandleFunc("POST /api/v1/agents/preflight/greenlight", h.greenlight)
	mux.HandleFunc("POST /api/v1/agents/restore-done", h.restoreDone)
	mux.HandleFunc("POST /api/v1/agents/apply-started", h.applyStarted)
	mux.HandleFunc("POST /api/v1/agents/apply-failed", h.applyFailed)
	mux.HandleFunc("POST /api/v1/agents/applied", h.applied)
	mux.HandleFunc("POST /api/v1/agents/ready", h.ready)
	mux.HandleFunc("POST /api/v1/clusters/{id}/set-ready", h.submitSetReady)
	mux.HandleFunc("GET /api/v1/clusters/{id}/set-ready", h.getSetReady)
	mux.HandleFunc("POST /api/v1/clusters/{id}/deploy/preflight/rekick/{hostname}", h.rekickPreflight)
	mux.HandleFunc("POST /api/v1/clusters/{id}/deploy/step/next", h.advanceStep)
	mux.HandleFunc("POST /api/v1/machines/inspect", h.startInspect)
	mux.HandleFunc("GET /api/v1/machines/inspect", h.inspectStatus)
}

// resolveAssignment picks the machine's assignment in the cluster whose deploy
// is actively running — the deploy that PXE-booted it. Falls back to the
// primary (first) assignment when no assigned cluster is deploying.
func (h *deployHandlers) resolveAssignment(m *inventory.Machine) *inventory.Assignment {
	for i := range m.Assignments {
		if h.mgr.HasActiveDeploy(m.Assignments[i].ClusterID) {
			return &m.Assignments[i]
		}
	}
	return m.Assignment
}

// normSerial normalizes a board/DMI serial for comparison. IPMI FRU reads pad
// the field with trailing NUL bytes (e.g. "G7Q1JD2\x00\x00…"), which TrimSpace
// does not remove — so an exact match against the agent's clean serial silently
// fails. Strip NULs, trim, and lowercase both sides.
func normSerial(s string) string {
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(s, "\x00", "")))
}

// matchAnyMachine finds a machine by NIC MAC or serial regardless of whether it
// has an assignment (used for inspect boots, which precede assignment).
func (h *deployHandlers) matchAnyMachine(macs []string, serial string) *inventory.Machine {
	want := map[string]bool{}
	for _, m := range macs {
		want[strings.ToLower(strings.TrimSpace(m))] = true
	}
	serial = normSerial(serial)
	machines, err := h.machines.List()
	if err != nil {
		return nil
	}
	for i := range machines {
		for _, mac := range macsOf(machines[i]) {
			if want[strings.ToLower(mac)] {
				return &machines[i]
			}
		}
		if serial != "" && machines[i].Inventory != nil &&
			normSerial(machines[i].Inventory.Serial) == serial {
			return &machines[i]
		}
	}
	return nil
}

// matchNode finds the assigned machine for a checking-in node by NIC MAC, and
// falls back to the DMI/board serial. IPMI/Redfish inventory carries the board
// serial but no NIC MACs, so serial is the only handle when that is the
// discovery source. The returned assignment is resolved against the active
// deploy (resolveAssignment) — callers must use it, not machine.Assignment.
func (h *deployHandlers) matchNode(macs []string, serial string) (*inventory.Machine, *inventory.Assignment, error) {
	want := map[string]bool{}
	for _, m := range macs {
		want[strings.ToLower(strings.TrimSpace(m))] = true
	}
	serial = normSerial(serial)
	machines, err := h.machines.List()
	if err != nil {
		return nil, nil, err
	}
	for i := range machines {
		if machines[i].Assignment == nil {
			continue
		}
		for _, mac := range macsOf(machines[i]) {
			if want[strings.ToLower(mac)] {
				return &machines[i], h.resolveAssignment(&machines[i]), nil
			}
		}
		if serial != "" && machines[i].Inventory != nil &&
			normSerial(machines[i].Inventory.Serial) == serial {
			return &machines[i], h.resolveAssignment(&machines[i]), nil
		}
	}
	return nil, nil, nil
}

type planRow struct {
	Hostname     string   `json:"hostname"`
	Assigned     bool     `json:"assigned"`
	MachineLabel string   `json:"machineLabel,omitempty"`
	BMCAddress   string   `json:"bmcAddress,omitempty"`
	OSDisk       string   `json:"osDisk,omitempty"`
	MACs         []string `json:"macs,omitempty"`
	// Conflict names another cluster whose ACTIVE deploy already claims this
	// row's machine — deploying now would race it, so start refuses (409).
	Conflict string `json:"conflict,omitempty"`
}

// clusterName resolves a cluster's display name, falling back to its ID.
func (h *deployHandlers) clusterName(id string) string {
	if d, err := h.clusters.Detail(id); err == nil && d.ClusterInfo.Name != "" {
		return d.ClusterInfo.Name
	}
	return id
}

// activeConflict returns the name of another cluster whose active deploy
// claims this machine (empty if none).
func (h *deployHandlers) activeConflict(m inventory.Machine, id string) string {
	for _, a := range m.Assignments {
		if a.ClusterID != id && h.mgr.HasActiveDeploy(a.ClusterID) {
			return h.clusterName(a.ClusterID)
		}
	}
	return ""
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
		if a := m.AssignmentFor(id); a != nil {
			byHost[a.Hostname] = m
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
		osDisk := ""
		if a := m.AssignmentFor(id); a != nil {
			osDisk = a.OSDisk
		}
		macs := macsOf(m)
		row.MachineLabel = m.Label
		row.BMCAddress = addr
		row.OSDisk = osDisk
		row.MACs = macs
		row.Conflict = h.activeConflict(m, id)
		rows = append(rows, row)
		nodes = append(nodes, orchestrator.Node{
			Hostname:   node.Hostname,
			MachineID:  m.ID,
			BMCAddress: addr,
			BMCUser:    user,
			BMCPass:    pass,
			MACs:       macs,
			OSDisk:     osDisk,
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
		Manual    bool     `json:"manual"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if !body.Confirm {
		writeError(w, http.StatusBadRequest, "deploy requires confirm=true")
		return
	}
	nodes, rows, allAssigned, master, err := h.buildNodes(id)
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
	// Refuse to race another cluster's active deploy for the same hardware —
	// its check-ins would resolve to that deploy, not this one.
	for _, row := range rows {
		if row.Conflict != "" {
			writeError(w, http.StatusConflict,
				"machine %s (node %s) is in an active deploy for cluster %s — wait for it to finish or cancel it",
				row.MachineLabel, row.Hostname, row.Conflict)
			return
		}
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
	dep, err := h.mgr.Start(id, nodes, master, h.verifyTargets(id), body.Manual)
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

// release manually releases one node's apply gate (manual one-by-one reimage):
// writes the master-done "go" SEL to that node's BMC so it applies.
func (h *deployHandlers) release(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	host, ok := pathValue(w, r, "hostname")
	if !ok {
		return
	}
	if err := h.mgr.ReleaseNode(id, host); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "released", "hostname": host})
}

// checkin matches an agent's MACs to an appointment and returns its snapshot
// URL + preflight targets, advancing that node to checked-in.
func (h *deployHandlers) checkin(w http.ResponseWriter, r *http.Request) {
	var req agent.CheckinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	matched, assn, err := h.matchNode(req.MACs, req.Serial)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if matched == nil {
		log.Printf("checkin UNMATCHED: macs=%v serial=%q", req.MACs, req.Serial)
		writeJSON(w, http.StatusOK, agent.CheckinResponse{Appointed: false})
		return
	}

	cid := assn.ClusterID
	// Same safety gate as preflightCheckin: only appoint (→ download + apply)
	// while a deploy is actively running for this cluster.
	if !h.mgr.HasActiveDeploy(cid) {
		log.Printf("checkin: %s assigned to %s but no active deploy — holding (not appointed)", matched.Label, cid)
		writeJSON(w, http.StatusOK, agent.CheckinResponse{Appointed: false})
		return
	}
	host := assn.Hostname
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
// startInspect force-PXEs the selected machines into an inventory-only boot
// (agent --inventory: report hardware, then halt) so the assign flow has real
// CPU/mem/disk/NIC to work with. Power-cycles the boxes.
func (h *deployHandlers) startInspect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	var nodes []orchestrator.Node
	labels := map[string]string{}
	for _, id := range req.IDs {
		addr, user, pass, cerr := h.machines.Credentials(id)
		if cerr != nil {
			continue
		}
		m, gerr := h.machines.Get(id)
		if gerr != nil {
			continue
		}
		nodes = append(nodes, orchestrator.Node{MachineID: id, BMCAddress: addr, BMCUser: user, BMCPass: pass})
		labels[id] = m.Label
	}
	if len(nodes) == 0 {
		writeError(w, http.StatusBadRequest, "no inspectable machines")
		return
	}
	log.Printf("inspect: starting %d machine(s)", len(nodes))
	h.mgr.StartInspect(nodes, labels)
	writeJSON(w, http.StatusAccepted, map[string]int{"started": len(nodes)})
}

// inspectStatus returns the current inspect-boot progress (for the UI poll).
func (h *deployHandlers) inspectStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.mgr.Inspects())
}

// submitSetReady stores the operator's UI set_ready input (external network +
// CIDR/gateway/pool) and arms the trigger the master agent polls for.
func (h *deployHandlers) submitSetReady(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	var in orchestrator.SetReadyInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	h.mgr.SubmitSetReady(id, in)
	log.Printf("set-ready submitted for %s (external=%v cidr=%q)", id, in.CreateExternal, in.CIDR)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// getSetReady returns the set_ready input/status (agent polls; UI reads).
func (h *deployHandlers) getSetReady(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.mgr.GetSetReady(id))
}

// ready records the master's set_ready result.
func (h *deployHandlers) ready(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClusterID string `json:"clusterId"`
		Hostname  string `json:"hostname"`
		OK        bool   `json:"ok"`
		Message   string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	log.Printf("set_ready result for %s: ok=%v %s", req.ClusterID, req.OK, req.Message)
	h.mgr.MarkReady(req.ClusterID, req.OK, req.Message)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// applied is reported by the OS-phase agent after it applies its local snapshot.
// When the master reports, the manager releases the non-masters via SEL.
func (h *deployHandlers) applied(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClusterID string   `json:"clusterId"`
		Hostname  string   `json:"hostname"`
		IsMaster  bool     `json:"isMaster"`
		MACs      []string `json:"macs"`
		Serial    string   `json:"serial"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	cid, host := req.ClusterID, req.Hostname
	if m, a, _ := h.matchNode(req.MACs, req.Serial); m != nil && a != nil {
		cid, host = a.ClusterID, a.Hostname
	}
	log.Printf("applied: host=%s cluster=%s master=%v", host, cid, req.IsMaster)
	h.mgr.Applied(cid, host, req.IsMaster)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// restoreDone is reported in-band by the installer just before it reboots the
// imaged node, so the progress strip shows restore-complete / reboot distinctly.
func (h *deployHandlers) restoreDone(w http.ResponseWriter, r *http.Request) {
	var req agent.CheckinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	matched, assn, err := h.matchNode(req.MACs, req.Serial)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if matched == nil || assn == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": false})
		return
	}
	log.Printf("restore-done for %s (serial=%q)", assn.Hostname, req.Serial)
	h.mgr.RestoreDone(assn.ClusterID, assn.Hostname)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proceed": h.mgr.RebootProceed(assn.ClusterID)})
}

// applyStarted is reported by the OS-phase agent the moment it comes up (reboot
// complete), before the snapshot apply — flips the node reboot→done, apply→active.
func (h *deployHandlers) applyStarted(w http.ResponseWriter, r *http.Request) {
	var req agent.CheckinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	matched, assn, err := h.matchNode(req.MACs, req.Serial)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if matched == nil || assn == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": false})
		return
	}
	log.Printf("apply-started for %s (serial=%q)", assn.Hostname, req.Serial)
	h.mgr.ApplyStarted(assn.ClusterID, assn.Hostname)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proceed": h.mgr.ApplyProceed(assn.ClusterID, assn.Hostname)})
}

// applyFailed is reported by the OS-phase agent when the snapshot apply fails
// terminally (real failure, or did not converge after the bounded reboots) —
// marks the node errored so the UI stops showing rebooting/applying.
func (h *deployHandlers) applyFailed(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClusterID string   `json:"clusterId"`
		Hostname  string   `json:"hostname"`
		Message   string   `json:"message"`
		MACs      []string `json:"macs"`
		Serial    string   `json:"serial"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	cid, host := req.ClusterID, req.Hostname
	if m, a, _ := h.matchNode(req.MACs, req.Serial); m != nil && a != nil {
		cid, host = a.ClusterID, a.Hostname
	}
	log.Printf("apply-failed: host=%s cluster=%s msg=%q", host, cid, req.Message)
	h.mgr.ApplyFailed(cid, host, req.Message)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *deployHandlers) preflightCheckin(w http.ResponseWriter, r *http.Request) {
	var req agent.CheckinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	// Inspect boot: a machine force-PXEd for hardware discovery (even if it has
	// no assignment yet) reports inventory + halts instead of deploying.
	if am := h.matchAnyMachine(req.MACs, req.Serial); am != nil && h.mgr.IsInspecting(am.ID) {
		log.Printf("preflight checkin: %s is inspecting — inventory mode", am.Label)
		writeJSON(w, http.StatusOK, agent.PreflightCheckinResponse{Inspect: true})
		return
	}
	matched, assn, err := h.matchNode(req.MACs, req.Serial)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if matched == nil {
		log.Printf("preflight checkin UNMATCHED: macs=%v serial=%q", req.MACs, req.Serial)
		writeJSON(w, http.StatusOK, agent.PreflightCheckinResponse{Appointed: false})
		return
	}
	cid := assn.ClusterID
	// Safety gate: a node restores/reimages ONLY while a deploy is actively
	// running for its cluster. An assigned node that PXE-boots for any other
	// reason (an inspect, a stray netboot, a manual reboot) must never wipe
	// itself — hold it as not-appointed until the operator starts a deploy.
	if !h.mgr.HasActiveDeploy(cid) {
		log.Printf("preflight checkin: %s assigned to %s but no active deploy — holding (not appointed)", matched.Label, cid)
		writeJSON(w, http.StatusOK, agent.PreflightCheckinResponse{Appointed: false})
		return
	}
	log.Printf("preflight checkin matched %s: macs=%v serial=%q", assn.Hostname, req.MACs, req.Serial)
	host := assn.Hostname
	detail, err := h.clusters.Detail(cid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	bundle := generator.BuildPreflightBundles(detail)[host]
	h.mgr.PreflightProgress(cid, host)
	scheme := "http://"
	if r.TLS != nil {
		scheme = "https://"
	}
	writeJSON(w, http.StatusOK, agent.PreflightCheckinResponse{
		Appointed:     true,
		ClusterID:     cid,
		Hostname:      host,
		ServerTimeUTC: time.Now().UTC().Format(time.RFC3339),
		Bundle:        bundle,
		SnapshotURL:   scheme + r.Host + "/api/v1/clusters/" + cid + "/nodes/" + host + "/download",
		IsMaster:      host == h.mgr.Master(cid),
		OSDisk:        assn.OSDisk,
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
	// The response tells the parked agent whether the operator requested an
	// in-place preflight re-run (seq bump) — no PXE reboot needed.
	writeJSON(w, http.StatusOK, agent.PreflightReportResponse{
		Message:   "ok",
		RekickSeq: h.mgr.RekickSeq(req.ClusterID, req.Hostname),
	})
}

// rekickPreflight asks a node's parked installer agent to redo preflight from
// check-in — after the operator fixes the cluster config/snapshot.
// advanceStep moves a manual deploy to the next gated step (operator "Next").
func (h *deployHandlers) advanceStep(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	step, err := h.mgr.AdvanceStep(id)
	if err != nil {
		writeError(w, http.StatusConflict, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "advanced", "manualStep": step})
}

func (h *deployHandlers) rekickPreflight(w http.ResponseWriter, r *http.Request) {
	id, ok := pathValue(w, r, "id")
	if !ok {
		return
	}
	host, ok := pathValue(w, r, "hostname")
	if !ok {
		return
	}
	if err := h.mgr.RekickPreflight(id, host); err != nil {
		writeError(w, http.StatusConflict, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "preflight re-run requested", "rekickSeq": h.mgr.RekickSeq(id, host)})
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
