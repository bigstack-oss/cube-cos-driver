package api

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bigstack-oss/cube-cos-snapshot/internal/inventory"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/model"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/orchestrator"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/secret"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/storage"
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
