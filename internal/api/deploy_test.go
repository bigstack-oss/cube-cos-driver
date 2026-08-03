package api

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bigstack-oss/cube-cos-driver/internal/inventory"
	"github.com/bigstack-oss/cube-cos-driver/internal/model"
	"github.com/bigstack-oss/cube-cos-driver/internal/orchestrator"
	"github.com/bigstack-oss/cube-cos-driver/internal/secret"
	"github.com/bigstack-oss/cube-cos-driver/internal/storage"
)

const depClusterID = "aabbccddee01"

// deployFixture sets up a server whose ha3 cluster has all 3 nodes assigned to
// fetched machines (with MACs), backed by the same on-disk stores.
func deployFixture(t *testing.T, exec orchestrator.Executor) (*httptest.Server, *orchestrator.FakeExecutor) {
	t.Helper()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, ".key")

	box, err := secret.Load("", keyFile)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := inventory.NewStore(filepath.Join(dir, "machines"), box)
	if err != nil {
		t.Fatal(err)
	}
	// Save the ha3 cluster (generates its snapshots too).
	raw, _ := os.ReadFile("../model/testdata/ha3.json")
	cs := &storage.Store{DataDir: dir}
	var detail struct {
		NodeData []struct {
			Hostname string `json:"hostname"`
		} `json:"nodeData"`
	}
	json.Unmarshal(raw, &detail)
	var clusterDetail = mustClusterDetail(t, raw)
	if err := cs.Save(clusterDetail); err != nil {
		t.Fatal(err)
	}
	// One machine per node, each with a distinct MAC, assigned to its node.
	for i, n := range detail.NodeData {
		m, err := inv.Create(inventory.Input{Label: n.Hostname, Address: "10.0.0." + itoa(i+1), Username: "admin"})
		if err != nil {
			t.Fatal(err)
		}
		inv.SetInventory(m.ID, inventory.Inventory{
			Source: "test",
			NICs:   []inventory.NIC{{Name: "eth0", MAC: macFor(i)}},
			Disks:  []inventory.Disk{{Name: "sda"}},
		})
		if _, err := inv.Assign(m.ID, depClusterID, n.Hostname, "sda"); err != nil {
			t.Fatal(err)
		}
	}

	fake, _ := exec.(*orchestrator.FakeExecutor)
	srv := newTestServerCfg(t, Config{
		DataDir:        dir,
		SecretKeyFile:  keyFile,
		DeployExecutor: exec,
		DeployConfig:   orchestrator.Config{PollInterval: time.Millisecond, StageTimeout: 2 * time.Second},
	})
	return srv, fake
}

func TestDeployPlanAndRun(t *testing.T) {
	srv, _ := deployFixture(t, orchestrator.NewFakeExecutor())

	// Plan: all assigned → 200.
	resp := do(t, "GET", srv.URL+"/api/v1/clusters/"+depClusterID+"/deploy/plan", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("plan = %d", resp.StatusCode)
	}
	var plan struct {
		AllAssigned bool `json:"allAssigned"`
		Nodes       []struct {
			Hostname string   `json:"hostname"`
			Assigned bool     `json:"assigned"`
			MACs     []string `json:"macs"`
		} `json:"nodes"`
	}
	json.NewDecoder(resp.Body).Decode(&plan)
	resp.Body.Close()
	if !plan.AllAssigned || len(plan.Nodes) != 3 {
		t.Fatalf("plan = %+v", plan)
	}

	// Deploy without confirm → 400.
	resp = do(t, "POST", srv.URL+"/api/v1/clusters/"+depClusterID+"/deploy", []byte(`{}`))
	if resp.StatusCode != 400 {
		t.Fatalf("no-confirm = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Deploy with confirm → 202.
	resp = do(t, "POST", srv.URL+"/api/v1/clusters/"+depClusterID+"/deploy", []byte(`{"confirm":true}`))
	if resp.StatusCode != 202 {
		t.Fatalf("deploy = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Nodes reach imaged.
	waitNode(t, srv, "cube-1", "imaged")

	// Agent for cube-1 checks in (MAC of machine 0) → appointed.
	ci := do(t, "POST", srv.URL+"/api/v1/agents/checkin", []byte(`{"macs":["`+macFor(0)+`"],"serial":"S"}`))
	var cr struct {
		Appointed   bool   `json:"appointed"`
		ClusterID   string `json:"clusterId"`
		Hostname    string `json:"hostname"`
		SnapshotURL string `json:"snapshotUrl"`
		Preflight   struct {
			Gateway string   `json:"gateway"`
			DNS     []string `json:"dns"`
			Peers   []string `json:"peers"`
		} `json:"preflight"`
	}
	json.NewDecoder(ci.Body).Decode(&cr)
	ci.Body.Close()
	if !cr.Appointed || cr.Hostname != "cube-1" || cr.ClusterID != depClusterID {
		t.Fatalf("checkin = %+v", cr)
	}
	if cr.Preflight.Gateway == "" || len(cr.Preflight.DNS) == 0 {
		t.Fatalf("preflight targets missing: %+v", cr.Preflight)
	}

	// Agent reports done.
	do(t, "POST", srv.URL+"/api/v1/agents/report", []byte(`{"clusterId":"`+depClusterID+`","hostname":"cube-1","state":"done","message":"applied"}`)).Body.Close()
	waitNode(t, srv, "cube-1", "done")
}

func TestDeployPlan409WhenUnassigned(t *testing.T) {
	// Save ha3 but assign nothing.
	dir := t.TempDir()
	keyFile := filepath.Join(dir, ".key")
	raw, _ := os.ReadFile("../model/testdata/ha3.json")
	cs := &storage.Store{DataDir: dir}
	cs.Save(mustClusterDetail(t, raw))
	srv := newTestServerCfg(t, Config{DataDir: dir, SecretKeyFile: keyFile, DeployExecutor: orchestrator.NewFakeExecutor()})

	resp := do(t, "GET", srv.URL+"/api/v1/clusters/"+depClusterID+"/deploy/plan", nil)
	if resp.StatusCode != 409 {
		t.Fatalf("expected 409 unassigned, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Start also blocked.
	resp = do(t, "POST", srv.URL+"/api/v1/clusters/"+depClusterID+"/deploy", []byte(`{"confirm":true}`))
	if resp.StatusCode != 409 {
		t.Fatalf("expected 409 on start, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func waitNode(t *testing.T, srv *httptest.Server, host, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp := do(t, "GET", srv.URL+"/api/v1/clusters/"+depClusterID+"/deploy", nil)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var dep struct {
			Nodes map[string]struct {
				State string `json:"state"`
			} `json:"nodes"`
		}
		json.Unmarshal(body, &dep)
		if dep.Nodes[host].State == want {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("node %s did not reach %s", host, want)
}

func macFor(i int) string { return "aa:bb:cc:00:00:0" + itoa(i+1) }
func itoa(i int) string   { return string(rune('0' + i)) }

func mustClusterDetail(t *testing.T, raw []byte) model.ClusterDetail {
	t.Helper()
	var d model.ClusterDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPreflightEndpoints(t *testing.T) {
	srv, _ := deployFixture(t, orchestrator.NewFakeExecutor())

	// Start the deploy so the manager has a Deploy for the cluster.
	do(t, "POST", srv.URL+"/api/v1/clusters/"+depClusterID+"/deploy", []byte(`{"confirm":true}`)).Body.Close()

	// Installer-phase check-in by MAC → appointed with a topology+peer bundle.
	ci := do(t, "POST", srv.URL+"/api/v1/agents/preflight/checkin", []byte(`{"macs":["`+macFor(0)+`"],"serial":"S"}`))
	var pc struct {
		Appointed     bool                  `json:"appointed"`
		Hostname      string                `json:"hostname"`
		ServerTimeUTC string                `json:"serverTimeUTC"`
		Bundle        model.PreflightBundle `json:"bundle"`
	}
	json.NewDecoder(ci.Body).Decode(&pc)
	ci.Body.Close()
	if !pc.Appointed || pc.Hostname != "cube-1" {
		t.Fatalf("preflight checkin = %+v", pc)
	}
	if len(pc.Bundle.Links) == 0 || pc.Bundle.Hostname != "cube-1" {
		t.Fatalf("bundle missing links: %+v", pc.Bundle)
	}
	if pc.ServerTimeUTC == "" {
		t.Fatal("serverTimeUTC missing")
	}

	greenlight := func(host string) bool {
		r := do(t, "POST", srv.URL+"/api/v1/agents/preflight/greenlight",
			[]byte(`{"clusterId":"`+depClusterID+`","hostname":"`+host+`"}`))
		var g struct {
			Clear bool `json:"clear"`
		}
		json.NewDecoder(r.Body).Decode(&g)
		r.Body.Close()
		return g.Clear
	}
	report := func(host string, passed, carrier bool, skew float64) {
		body, _ := json.Marshal(agentPfReport{ClusterID: depClusterID, Hostname: host, CarrierOK: carrier, ClockSkewSec: skew, Passed: passed})
		do(t, "POST", srv.URL+"/api/v1/agents/preflight/report", body).Body.Close()
	}

	// Only cube-1 passed → green light 1 withheld.
	report("cube-1", true, true, 0.1)
	if greenlight("cube-1") {
		t.Fatal("GL1 must be withheld until every node passes")
	}
	// cube-2 passes but cube-3 has excessive skew → still withheld.
	report("cube-2", true, true, 0.1)
	report("cube-3", false, true, 9)
	if greenlight("cube-1") {
		t.Fatal("GL1 must be withheld while a node exceeds the skew gate")
	}
	// cube-3 re-kicks clean → GL1 clears.
	report("cube-3", true, true, 0.1)
	if !greenlight("cube-3") {
		t.Fatal("GL1 should clear once every node passes")
	}
	waitNode(t, srv, "cube-3", "restoring")
}

type agentPfReport struct {
	ClusterID    string  `json:"clusterId"`
	Hostname     string  `json:"hostname"`
	CarrierOK    bool    `json:"carrierOk"`
	ClockSkewSec float64 `json:"clockSkewSec"`
	Passed       bool    `json:"passed"`
}

const dep2ClusterID = "aabbccddee02"

// addSecondCluster saves a second cluster (ha3 with a different ID + name) and
// assigns every machine to its same-named node there too, so machines hold
// assignments in two clusters with dep2 as the NON-primary one.
func addSecondCluster(t *testing.T, srv *httptest.Server) {
	t.Helper()
	raw, _ := os.ReadFile("../model/testdata/ha3.json")
	mod := strings.Replace(string(raw), "aabbccddee01", "aabbccddee02", 1)
	mod = strings.Replace(mod, `"name": "sky-lab"`, `"name": "sky-lab-b"`, 1)
	resp := do(t, "PUT", srv.URL+"/api/v1/clusters/"+dep2ClusterID, []byte(mod))
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("save cluster2 = %d: %s", resp.StatusCode, b)
	}
	resp.Body.Close()
	resp = do(t, "GET", srv.URL+"/api/v1/machines", nil)
	var machines []struct {
		ID         string `json:"id"`
		Assignment *struct {
			Hostname string `json:"hostname"`
		} `json:"assignment"`
	}
	json.NewDecoder(resp.Body).Decode(&machines)
	resp.Body.Close()
	for _, m := range machines {
		if m.Assignment == nil {
			continue
		}
		body := []byte(`{"clusterId":"` + dep2ClusterID + `","hostname":"` + m.Assignment.Hostname + `","osDisk":"sda"}`)
		ar := do(t, "PUT", srv.URL+"/api/v1/machines/"+m.ID+"/assignment", body)
		if ar.StatusCode != 200 {
			t.Fatalf("assign to cluster2 = %d", ar.StatusCode)
		}
		ar.Body.Close()
	}
}

func waitNodeIn(t *testing.T, srv *httptest.Server, cluster, host, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp := do(t, "GET", srv.URL+"/api/v1/clusters/"+cluster+"/deploy", nil)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var dep struct {
			Nodes map[string]struct {
				State string `json:"state"`
			} `json:"nodes"`
		}
		json.Unmarshal(body, &dep)
		if dep.Nodes[host].State == want {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("node %s did not reach %s in %s", host, want, cluster)
}

// A machine assigned to two clusters must check in against the cluster whose
// deploy is actively running — not blindly its primary (first) assignment.
func TestCheckinResolvesActiveDeployCluster(t *testing.T) {
	srv, _ := deployFixture(t, orchestrator.NewFakeExecutor())
	addSecondCluster(t, srv)

	resp := do(t, "POST", srv.URL+"/api/v1/clusters/"+dep2ClusterID+"/deploy", []byte(`{"confirm":true}`))
	if resp.StatusCode != 202 {
		t.Fatalf("deploy cluster2 = %d", resp.StatusCode)
	}
	resp.Body.Close()
	waitNodeIn(t, srv, dep2ClusterID, "cube-1", "imaged")

	ci := do(t, "POST", srv.URL+"/api/v1/agents/checkin", []byte(`{"macs":["`+macFor(0)+`"],"serial":"S"}`))
	var cr struct {
		Appointed bool   `json:"appointed"`
		ClusterID string `json:"clusterId"`
		Hostname  string `json:"hostname"`
	}
	json.NewDecoder(ci.Body).Decode(&cr)
	ci.Body.Close()
	if !cr.Appointed || cr.ClusterID != dep2ClusterID || cr.Hostname != "cube-1" {
		t.Fatalf("checkin should appoint to the actively-deploying cluster2, got %+v", cr)
	}
}

// Starting a deploy whose machines are already claimed by another cluster's
// active deploy must be refused (409), and the plan must pre-flag the conflict.
func TestDeployStartConflictsWithActiveDeploy(t *testing.T) {
	srv, _ := deployFixture(t, orchestrator.NewFakeExecutor())
	addSecondCluster(t, srv)

	resp := do(t, "POST", srv.URL+"/api/v1/clusters/"+depClusterID+"/deploy", []byte(`{"confirm":true}`))
	if resp.StatusCode != 202 {
		t.Fatalf("deploy cluster1 = %d", resp.StatusCode)
	}
	resp.Body.Close()

	pr := do(t, "GET", srv.URL+"/api/v1/clusters/"+dep2ClusterID+"/deploy/plan", nil)
	var plan struct {
		Nodes []struct {
			Hostname string `json:"hostname"`
			Conflict string `json:"conflict"`
		} `json:"nodes"`
	}
	json.NewDecoder(pr.Body).Decode(&plan)
	pr.Body.Close()
	for _, n := range plan.Nodes {
		if n.Conflict == "" {
			t.Fatalf("plan row %s should flag the active-deploy conflict", n.Hostname)
		}
	}

	dr := do(t, "POST", srv.URL+"/api/v1/clusters/"+dep2ClusterID+"/deploy", []byte(`{"confirm":true}`))
	if dr.StatusCode != 409 {
		t.Fatalf("deploy cluster2 while cluster1 active = %d, want 409", dr.StatusCode)
	}
	b, _ := io.ReadAll(dr.Body)
	dr.Body.Close()
	if !strings.Contains(string(b), "sky-lab") {
		t.Fatalf("409 should name the conflicting cluster, got %s", b)
	}
}

// The driver is authoritative on who is master: apply-started returns isMaster
// from d.Master (not the agent's fragile IS_MASTER env), so a sole master can't
// be stranded as a peer. Master → isMaster true + not "waiting"; peer → false.
func TestApplyStartedReturnsMasterFromDriver(t *testing.T) {
	srv, _ := deployFixture(t, orchestrator.NewFakeExecutor())
	do(t, "POST", srv.URL+"/api/v1/clusters/"+depClusterID+"/deploy", []byte(`{"confirm":true}`)).Body.Close()

	st := do(t, "GET", srv.URL+"/api/v1/clusters/"+depClusterID+"/deploy", nil)
	var status struct {
		Master string `json:"master"`
	}
	json.NewDecoder(st.Body).Decode(&status)
	st.Body.Close()
	if status.Master == "" {
		t.Fatal("deploy has no master")
	}

	// macFor(i) corresponds to NodeData[i] (fixture assignment order).
	raw, _ := os.ReadFile("../model/testdata/ha3.json")
	var d struct {
		NodeData []struct {
			Hostname string `json:"hostname"`
		} `json:"nodeData"`
	}
	json.Unmarshal(raw, &d)
	macOf := func(host string) string {
		for i, n := range d.NodeData {
			if n.Hostname == host {
				return macFor(i)
			}
		}
		return ""
	}
	applyStarted := func(mac string) bool {
		r := do(t, "POST", srv.URL+"/api/v1/agents/apply-started", []byte(`{"macs":["`+mac+`"],"serial":"S"}`))
		var out struct {
			OK       bool `json:"ok"`
			IsMaster bool `json:"isMaster"`
		}
		json.NewDecoder(r.Body).Decode(&out)
		r.Body.Close()
		if !out.OK {
			t.Fatalf("apply-started not ok for mac %s", mac)
		}
		return out.IsMaster
	}

	if !applyStarted(macOf(status.Master)) {
		t.Fatalf("master %s: apply-started returned isMaster=false", status.Master)
	}
	for _, n := range d.NodeData {
		if n.Hostname != status.Master {
			if applyStarted(macOf(n.Hostname)) {
				t.Fatalf("peer %s: apply-started returned isMaster=true", n.Hostname)
			}
			break
		}
	}
}
