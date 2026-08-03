package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const ha3ID = "aabbccddee01"

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return newTestServerCfg(t, Config{DataDir: t.TempDir()})
}

func newTestServerCfg(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	if cfg.DataDir == "" {
		cfg.DataDir = t.TempDir()
	}
	// create/update now verify the BMC via the discoverer; default to a
	// succeeding fake so tests that add machines don't need a real BMC.
	if cfg.Discoverer == nil {
		cfg.Discoverer = fakeDiscoverer{}
	}
	handler, mgr, err := newHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	// Stop deploy background goroutines before t.TempDir removal, else they can
	// still write the deploy store and fail cleanup with "directory not empty".
	t.Cleanup(mgr.StopAll)
	return srv
}

func do(t *testing.T, method, url string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestClustersCRUD(t *testing.T) {
	srv := newTestServer(t)
	fixture, err := os.ReadFile("../model/testdata/ha3.json")
	if err != nil {
		t.Fatal(err)
	}

	// PUT
	resp := do(t, "PUT", srv.URL+"/api/v1/clusters/"+ha3ID, fixture)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT = %d: %s", resp.StatusCode, b)
	}
	resp.Body.Close()

	// List
	resp = do(t, "GET", srv.URL+"/api/v1/clusters", nil)
	var digests []struct {
		ID    string   `json:"id"`
		Name  string   `json:"name"`
		Nodes []string `json:"nodes"`
	}
	json.NewDecoder(resp.Body).Decode(&digests)
	resp.Body.Close()
	if len(digests) != 1 || digests[0].ID != ha3ID || len(digests[0].Nodes) != 3 {
		t.Fatalf("digests = %+v", digests)
	}

	// Detail equals input (semantically)
	resp = do(t, "GET", srv.URL+"/api/v1/clusters/"+ha3ID, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var a, b any
	json.Unmarshal(fixture, &a)
	json.Unmarshal(got, &b)
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	if !bytes.Equal(aj, bj) {
		t.Fatal("detail does not round-trip")
	}

	// Cluster zip download
	resp = do(t, "GET", srv.URL+"/api/v1/clusters/"+ha3ID+"/download", nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("zip download: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="sky-lab.zip"`) {
		t.Fatalf("disposition = %q", cd)
	}
	zb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if _, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb))); err != nil {
		t.Fatalf("not a zip: %v", err)
	}

	// Node snapshot download
	resp = do(t, "GET", srv.URL+"/api/v1/clusters/"+ha3ID+"/nodes/cube-2/download", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("node download = %d", resp.StatusCode)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="cube-2.snapshot"`) {
		t.Fatalf("disposition = %q", cd)
	}
	resp.Body.Close()

	// DELETE
	resp = do(t, "DELETE", srv.URL+"/api/v1/clusters/"+ha3ID, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, "GET", srv.URL+"/api/v1/clusters/"+ha3ID, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("detail after delete = %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestBadRequests(t *testing.T) {
	srv := newTestServer(t)
	fixture, _ := os.ReadFile("../model/testdata/ha3.json")

	// id mismatch
	resp := do(t, "PUT", srv.URL+"/api/v1/clusters/wrongwrong12", fixture)
	if resp.StatusCode != 400 {
		t.Fatalf("mismatched id PUT = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// invalid body
	resp = do(t, "PUT", srv.URL+"/api/v1/clusters/"+ha3ID, []byte("{"))
	if resp.StatusCode != 400 {
		t.Fatalf("bad json PUT = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// path traversal-ish id
	resp = do(t, "GET", srv.URL+"/api/v1/clusters/%2e%2e%2fetc", nil)
	if resp.StatusCode != 400 && resp.StatusCode != 404 {
		t.Fatalf("traversal id = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// unknown API path → JSON 404, not SPA
	resp = do(t, "GET", srv.URL+"/api/v1/nope", nil)
	if resp.StatusCode != 404 {
		t.Fatalf("unknown api = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("unknown api content-type = %q", ct)
	}
	resp.Body.Close()

	// delete missing
	resp = do(t, "DELETE", srv.URL+"/api/v1/clusters/nope00000000", nil)
	if resp.StatusCode != 404 {
		t.Fatalf("delete missing = %d", resp.StatusCode)
	}
	resp.Body.Close()
}
