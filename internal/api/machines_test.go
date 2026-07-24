package api

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/bigstack-oss/cube-cos-snapshot/internal/discovery"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/inventory"
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
	srv := newTestServerCfg(t, Config{DataDir: t.TempDir()})

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

func TestMachineFetchError(t *testing.T) {
	srv := newTestServerCfg(t, Config{
		DataDir:    t.TempDir(),
		Discoverer: fakeDiscoverer{err: errDiscovery},
	})
	resp := do(t, "POST", srv.URL+"/api/v1/machines", []byte(`{"label":"n","bmc":{"address":"1.2.3.4","username":"u","password":"p"}}`))
	created := decodeMachine(t, resp.Body)
	resp.Body.Close()

	do(t, "POST", srv.URL+"/api/v1/machines/"+created.ID+"/fetch", nil).Body.Close()

	deadline := time.Now().Add(3 * time.Second)
	var m inventory.Machine
	for time.Now().Before(deadline) {
		resp = do(t, "GET", srv.URL+"/api/v1/machines/"+created.ID, nil)
		m = decodeMachine(t, resp.Body)
		resp.Body.Close()
		if m.FetchState == inventory.FetchError {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if m.FetchState != inventory.FetchError || !strings.Contains(m.FetchError, "boom") {
		t.Fatalf("expected fetch error, got %+v", m)
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
