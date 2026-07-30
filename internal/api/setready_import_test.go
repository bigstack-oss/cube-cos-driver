package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
)

// A cluster definition carrying setReady settings seeds the finalize spec on
// save (the import case), and a set-ready submit mirrors its parameters back
// into the stored definition (the export case).
func TestSetReadyTravelsWithCluster(t *testing.T) {
	srv := newTestServer(t)
	fixture, err := os.ReadFile("../model/testdata/ha3.json")
	if err != nil {
		t.Fatal(err)
	}

	// Import: inject setReady into the definition and save it.
	var d map[string]any
	if err := json.Unmarshal(fixture, &d); err != nil {
		t.Fatal(err)
	}
	cfg := d["clusterConfig"].(map[string]any)
	cfg["setReady"] = map[string]any{
		"createExternal": true,
		"cidr":           "10.32.0.0/16",
		"gateway":        "10.32.0.254",
		"ipRange":        "10.32.1.100-10.32.1.120",
	}
	body, _ := json.Marshal(d)
	if resp := do(t, "PUT", srv.URL+"/api/v1/clusters/"+ha3ID, body); resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT = %d: %s", resp.StatusCode, b)
	}

	// The finalize spec is seeded and armed.
	var sr struct {
		Trigger bool   `json:"trigger"`
		CIDR    string `json:"cidr"`
		Gateway string `json:"gateway"`
	}
	resp := do(t, "GET", srv.URL+"/api/v1/clusters/"+ha3ID+"/set-ready", nil)
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatal(err)
	}
	if !sr.Trigger || sr.CIDR != "10.32.0.0/16" || sr.Gateway != "10.32.0.254" {
		t.Fatalf("seeded set-ready = %+v", sr)
	}

	// A later save without setReady must not clobber the submitted spec.
	if resp := do(t, "PUT", srv.URL+"/api/v1/clusters/"+ha3ID, fixture); resp.StatusCode != 200 {
		t.Fatalf("re-save failed")
	}
	resp = do(t, "GET", srv.URL+"/api/v1/clusters/"+ha3ID+"/set-ready", nil)
	sr = struct {
		Trigger bool   `json:"trigger"`
		CIDR    string `json:"cidr"`
		Gateway string `json:"gateway"`
	}{}
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatal(err)
	}
	if !sr.Trigger || sr.CIDR != "10.32.0.0/16" {
		t.Fatalf("spec clobbered by re-save: %+v", sr)
	}

	// Export: submitting set-ready mirrors the parameters into the detail.
	sub := []byte(`{"trigger":true,"createExternal":false,"cidr":"172.16.0.0/16","gateway":"172.16.0.254","ipRange":"172.16.90.1-172.16.90.50"}`)
	if resp := do(t, "POST", srv.URL+"/api/v1/clusters/"+ha3ID+"/set-ready", sub); resp.StatusCode != 200 {
		t.Fatalf("submit set-ready failed")
	}
	var out struct {
		ClusterConfig struct {
			SetReady *struct {
				CIDR string `json:"cidr"`
			} `json:"setReady"`
		} `json:"clusterConfig"`
	}
	resp = do(t, "GET", srv.URL+"/api/v1/clusters/"+ha3ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET detail = %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.ClusterConfig.SetReady == nil || out.ClusterConfig.SetReady.CIDR != "172.16.0.0/16" {
		t.Fatalf("detail not mirrored: %+v", out.ClusterConfig.SetReady)
	}
}
