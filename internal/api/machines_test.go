package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bigstack-oss/cube-cos-driver/internal/discovery"
	"github.com/bigstack-oss/cube-cos-driver/internal/inventory"
)

type fakeDiscoverer struct {
	inv inventory.Inventory
	err error
}

func (f fakeDiscoverer) Discover(context.Context, discovery.Target) (inventory.Inventory, error) {
	return f.inv, f.err
}

func decodeMachine(t *testing.T, r io.Reader) inventory.Machine {
	t.Helper()
	var m inventory.Machine
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMachinesCRUD(t *testing.T) {
	// create/update verify the BMC synchronously, so a succeeding discoverer.
	srv := newTestServerCfg(t, Config{
		DataDir:    t.TempDir(),
		Discoverer: fakeDiscoverer{inv: inventory.Inventory{Serial: "SN"}},
	})

	// Create
	body := `{"label":"node-1","bmc":{"address":"10.0.0.1","username":"admin","password":"secret"}}`
	resp := do(t, "POST", srv.URL+"/api/v1/machines", []byte(body))
	if resp.StatusCode != 201 {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	created := decodeMachine(t, resp.Body)
	resp.Body.Close()
	if !created.HasPassword || created.BMC.Address != "10.0.0.1" {
		t.Fatalf("created = %+v", created)
	}

	// List
	resp = do(t, "GET", srv.URL+"/api/v1/machines", nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "passwordEnc") {
		t.Fatalf("list leaks secret: %s", raw)
	}

	// Update (no password -> keep)
	upd := `{"label":"renamed","bmc":{"address":"10.0.0.2","username":"admin"}}`
	resp = do(t, "PUT", srv.URL+"/api/v1/machines/"+created.ID, []byte(upd))
	if resp.StatusCode != 200 {
		t.Fatalf("update = %d", resp.StatusCode)
	}
	m := decodeMachine(t, resp.Body)
	resp.Body.Close()
	if m.Label != "renamed" || m.BMC.Address != "10.0.0.2" || !m.HasPassword {
		t.Fatalf("updated = %+v", m)
	}

	// Delete
	resp = do(t, "DELETE", srv.URL+"/api/v1/machines/"+created.ID, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, "GET", srv.URL+"/api/v1/machines/"+created.ID, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("get after delete = %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestMachineCreateValidation(t *testing.T) {
	srv := newTestServerCfg(t, Config{DataDir: t.TempDir()})
	resp := do(t, "POST", srv.URL+"/api/v1/machines", []byte(`{"label":"","bmc":{"address":""}}`))
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestMachineFetchSuccess(t *testing.T) {
	inv := inventory.Inventory{Source: "redfish", Serial: "SN123", CPUCount: 2, MemoryBytes: 1 << 34}
	srv := newTestServerCfg(t, Config{
		DataDir:    t.TempDir(),
		Discoverer: fakeDiscoverer{inv: inv},
	})

	resp := do(t, "POST", srv.URL+"/api/v1/machines", []byte(`{"label":"n","bmc":{"address":"10.0.0.1","username":"u","password":"p"}}`))
	created := decodeMachine(t, resp.Body)
	resp.Body.Close()

	resp = do(t, "POST", srv.URL+"/api/v1/machines/"+created.ID+"/fetch", nil)
	if resp.StatusCode != 202 {
		t.Fatalf("fetch = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Poll until the async fetch completes.
	deadline := time.Now().Add(3 * time.Second)
	var m inventory.Machine
	for time.Now().Before(deadline) {
		resp = do(t, "GET", srv.URL+"/api/v1/machines/"+created.ID, nil)
		m = decodeMachine(t, resp.Body)
		resp.Body.Close()
		if m.FetchState == inventory.FetchOK {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if m.FetchState != inventory.FetchOK || m.Inventory == nil || m.Inventory.Serial != "SN123" {
		t.Fatalf("fetch result = %+v", m)
	}
}

// Create verifies the BMC synchronously: an unreachable BMC / bad credentials
// (discoverer error) rejects the create and stores nothing.
func TestMachineCreateVerifyRejects(t *testing.T) {
	srv := newTestServerCfg(t, Config{
		DataDir:    t.TempDir(),
		Discoverer: fakeDiscoverer{err: errDiscovery},
	})
	resp := do(t, "POST", srv.URL+"/api/v1/machines", []byte(`{"label":"n","bmc":{"address":"1.2.3.4","username":"u","password":"p"}}`))
	if resp.StatusCode != 502 {
		t.Fatalf("create with failing BMC verify = %d, want 502", resp.StatusCode)
	}
	resp.Body.Close()

	// Nothing was stored.
	resp = do(t, "GET", srv.URL+"/api/v1/machines", nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("expected empty machine list, got %s", raw)
	}
}

func TestMachineFetchNotFound(t *testing.T) {
	srv := newTestServerCfg(t, Config{DataDir: t.TempDir()})
	resp := do(t, "POST", srv.URL+"/api/v1/machines/nope/fetch", nil)
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

var errDiscovery = discoveryError("boom")

type discoveryError string

func (e discoveryError) Error() string { return string(e) }

func TestMachineAssignment(t *testing.T) {
	srv := newTestServerCfg(t, Config{DataDir: t.TempDir()})
	create := func(label string) inventory.Machine {
		resp := do(t, "POST", srv.URL+"/api/v1/machines", []byte(`{"label":"`+label+`","bmc":{"address":"1.1.1.1"}}`))
		m := decodeMachine(t, resp.Body)
		resp.Body.Close()
		return m
	}
	a := create("a")
	b := create("b")

	// Assign a → cluster/node-1
	resp := do(t, "PUT", srv.URL+"/api/v1/machines/"+a.ID+"/assignment", []byte(`{"clusterId":"cl1","hostname":"node-1"}`))
	if resp.StatusCode != 200 {
		t.Fatalf("assign = %d", resp.StatusCode)
	}
	ma := decodeMachine(t, resp.Body)
	resp.Body.Close()
	if ma.Assignment == nil || ma.Assignment.Hostname != "node-1" {
		t.Fatalf("assignment = %+v", ma.Assignment)
	}

	// Assign b → same node moves it off a.
	do(t, "PUT", srv.URL+"/api/v1/machines/"+b.ID+"/assignment", []byte(`{"clusterId":"cl1","hostname":"node-1"}`)).Body.Close()
	resp = do(t, "GET", srv.URL+"/api/v1/machines/"+a.ID, nil)
	ga := decodeMachine(t, resp.Body)
	resp.Body.Close()
	if ga.Assignment != nil {
		t.Fatalf("machine a should be unassigned, got %+v", ga.Assignment)
	}

	// Missing fields → 400
	resp = do(t, "PUT", srv.URL+"/api/v1/machines/"+a.ID+"/assignment", []byte(`{"clusterId":"cl1"}`))
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Unassign b
	resp = do(t, "DELETE", srv.URL+"/api/v1/machines/"+b.ID+"/assignment", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("unassign = %d", resp.StatusCode)
	}
	gb := decodeMachine(t, resp.Body)
	resp.Body.Close()
	if gb.Assignment != nil {
		t.Fatalf("machine b should be unassigned, got %+v", gb.Assignment)
	}
}

func TestClusterDeleteClearsAssignments(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServerCfg(t, Config{DataDir: dir})
	// Create a machine and assign to a cluster id.
	resp := do(t, "POST", srv.URL+"/api/v1/machines", []byte(`{"label":"m","bmc":{"address":"1.1.1.1"}}`))
	m := decodeMachine(t, resp.Body)
	resp.Body.Close()
	do(t, "PUT", srv.URL+"/api/v1/machines/"+m.ID+"/assignment", []byte(`{"clusterId":"aabbccddee01","hostname":"cube-1"}`)).Body.Close()

	// PUT a cluster so it exists, then delete it.
	fixture, _ := os.ReadFile("../model/testdata/ha3.json")
	do(t, "PUT", srv.URL+"/api/v1/clusters/aabbccddee01", fixture).Body.Close()
	do(t, "DELETE", srv.URL+"/api/v1/clusters/aabbccddee01", nil).Body.Close()

	resp = do(t, "GET", srv.URL+"/api/v1/machines/"+m.ID, nil)
	gm := decodeMachine(t, resp.Body)
	resp.Body.Close()
	if gm.Assignment != nil {
		t.Fatalf("assignment should be cleared on cluster delete, got %+v", gm.Assignment)
	}
}

func TestMachineImportCSV(t *testing.T) {
	srv := newTestServerCfg(t, Config{DataDir: t.TempDir()})

	csv := "label,bmc_address,bmc_username,bmc_password\n" +
		"cube-1,10.0.0.11,admin,pw1\n" +
		"cube-2,10.0.0.12,root,pw2\n" +
		"bad,,admin,pw\n" // missing address

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "machines.csv")
	fw.Write([]byte(csv))
	mw.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/machines/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Created int `json:"created"`
		Errors  []struct {
			Row     int    `json:"row"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Created != 2 || len(out.Errors) != 1 {
		t.Fatalf("import result = %+v", out)
	}

	// The two good machines are now listed.
	resp = do(t, "GET", srv.URL+"/api/v1/machines", nil)
	var list []inventory.Machine
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 2 {
		t.Fatalf("listed %d machines", len(list))
	}
}

func TestMachineImportTemplate(t *testing.T) {
	srv := newTestServerCfg(t, Config{DataDir: t.TempDir()})
	resp := do(t, "GET", srv.URL+"/api/v1/machines/import/template", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("template = %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "bmc_address") {
		t.Fatalf("template missing header: %s", b)
	}
}
