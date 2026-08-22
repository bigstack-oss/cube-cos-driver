package api

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/bigstack-oss/cube-cos-driver/internal/clusterssh"
	"github.com/bigstack-oss/cube-cos-driver/internal/storage"
)

// enterpriseFixture saves an ha3 cluster with a VIP and stages the appfw
// artifacts its InstallParams reference, backed by a mock SSH dial.
func enterpriseFixture(t *testing.T) (srv *httptest.Server, id, dataDir string) {
	t.Helper()
	dataDir = t.TempDir()
	raw, err := os.ReadFile("../model/testdata/ha3.json")
	if err != nil {
		t.Fatal(err)
	}
	detail := mustClusterDetail(t, raw)
	detail.ClusterConfig.HASettings.VirtualIP = "10.32.10.140"
	cs := &storage.Store{DataDir: dataDir}
	if err := cs.Save(detail); err != nil {
		t.Fatal(err)
	}

	appfw := filepath.Join(dataDir, "enterprise", "appfw")
	if err := os.MkdirAll(appfw, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"r.raw", "m.qcow2", "a.qcow2"} {
		if err := os.WriteFile(filepath.Join(appfw, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	srv = newTestServerCfg(t, Config{
		DataDir: dataDir,
		EnterpriseDial: func(host, user, password string) (clusterssh.Client, error) {
			return &clusterssh.MockClient{}, nil
		},
	})
	return srv, detail.ShortID(), dataDir
}

type enterpriseInstall struct {
	ClusterID string `json:"ClusterID"`
	Module    string `json:"Module"`
	State     string `json:"State"`
	Steps     []struct {
		Name  string `json:"Name"`
		State string `json:"State"`
	} `json:"Steps"`
}

func TestEnterpriseInstallStartAndStatus(t *testing.T) {
	srv, id, _ := enterpriseFixture(t)

	body := []byte(`{"module":"appfw","manual":true,"params":{"Project":"cmp","PublicNet":"public","MgmtNet":"public","LBIP":"10.32.36.120","OSImage":"r.raw","FsImage":"m.qcow2","LBImage":"a.qcow2"}}`)
	resp := do(t, "POST", srv.URL+"/api/v1/clusters/"+id+"/enterprise/install", body)
	if resp.StatusCode != 202 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("start = %d: %s", resp.StatusCode, b)
	}
	var in enterpriseInstall
	json.NewDecoder(resp.Body).Decode(&in)
	resp.Body.Close()
	if in.Module != "appfw" || len(in.Steps) == 0 {
		t.Fatalf("start body = %+v", in)
	}

	resp = do(t, "GET", srv.URL+"/api/v1/clusters/"+id+"/enterprise/install?module=appfw", nil)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, b)
	}
	json.NewDecoder(resp.Body).Decode(&in)
	resp.Body.Close()
	if len(in.Steps) == 0 {
		t.Fatalf("status steps missing: %+v", in)
	}
}

func TestEnterpriseInstallStartSingleNodeNoVIP(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile("../model/testdata/ha3.json")
	if err != nil {
		t.Fatal(err)
	}
	detail := mustClusterDetail(t, raw)
	// A single-node (non-HA) cluster has no VIP; the driver must fall back to the
	// node's management IP for both the SSH target and the default password.
	detail.ClusterConfig.HASettings.VirtualIP = ""
	detail.ClusterConfig.HA = false
	detail.ClusterConfig.HASettings.VirtualHostname = ""
	cs := &storage.Store{DataDir: dir}
	if err := cs.Save(detail); err != nil {
		t.Fatal(err)
	}
	srv := newTestServerCfg(t, Config{
		DataDir: dir,
		EnterpriseDial: func(host, user, password string) (clusterssh.Client, error) {
			return &clusterssh.MockClient{}, nil
		},
	})

	resp := do(t, "POST", srv.URL+"/api/v1/clusters/"+detail.ShortID()+"/enterprise/install",
		[]byte(`{"module":"appfw","manual":true,"params":{"OSImage":"rancher.raw"}}`))
	if resp.StatusCode != 202 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("single-node (no VIP) start = %d, want 202: %s", resp.StatusCode, b)
	}
	resp.Body.Close()
}

// The advisor module must be accepted by the install allowlist, same as appfw/cmp.
func TestEnterpriseInstallStartAdvisorAccepted(t *testing.T) {
	srv, id, _ := enterpriseFixture(t)

	body := []byte(`{"module":"advisor","manual":true,"params":{"Project":"appfw","Framework":"appfw","OSImage":"r.raw","FsImage":"m.qcow2","LBImage":"a.qcow2","AdvisorFile":"cube-advisor-1.2.3.pigz","AdvisorLBIP":"10.0.0.9"}}`)
	resp := do(t, "POST", srv.URL+"/api/v1/clusters/"+id+"/enterprise/install", body)
	if resp.StatusCode != 202 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("start = %d: %s", resp.StatusCode, b)
	}
	var in enterpriseInstall
	json.NewDecoder(resp.Body).Decode(&in)
	resp.Body.Close()
	if in.Module != "advisor" || len(in.Steps) == 0 {
		t.Fatalf("start body = %+v", in)
	}
}

// An unknown module must be rejected before mgr.Start — otherwise BuildPlan
// silently degrades it to a preflight-only plan and the run gets persisted.
func TestEnterpriseInstallStartUnknownModuleRejected(t *testing.T) {
	srv, id, dataDir := enterpriseFixture(t)

	resp := do(t, "POST", srv.URL+"/api/v1/clusters/"+id+"/enterprise/install",
		[]byte(`{"module":"bogus","manual":true}`))
	if resp.StatusCode != 400 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("start with unknown module = %d: %s", resp.StatusCode, b)
	}
	resp.Body.Close()

	// No install was created for it.
	resp = do(t, "GET", srv.URL+"/api/v1/clusters/"+id+"/enterprise/install?module=bogus", nil)
	if resp.StatusCode != 404 {
		t.Fatalf("status for rejected module = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// No password sidecar was left behind either.
	if _, err := os.Stat(filepath.Join(dataDir, "installs", id+"-bogus.pw")); err == nil {
		t.Fatal("sidecar should not exist for a rejected module")
	}
}

// A missing OSImage must be rejected before mgr.Start — the plan imports the
// rancher image + framework_create keyed on it, and an empty value would scp
// the artifacts directory instead of a file.
func TestEnterpriseInstallStartRequiresOSImage(t *testing.T) {
	srv, id, _ := enterpriseFixture(t)

	resp := do(t, "POST", srv.URL+"/api/v1/clusters/"+id+"/enterprise/install",
		[]byte(`{"module":"appfw","manual":true}`))
	if resp.StatusCode != 400 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("start without OSImage = %d, want 400: %s", resp.StatusCode, b)
	}
	resp.Body.Close()
}

func TestEnterpriseArtifacts(t *testing.T) {
	dir := t.TempDir()
	appfw := filepath.Join(dir, "enterprise", "appfw")
	if err := os.MkdirAll(appfw, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appfw, "r.raw"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newTestServerCfg(t, Config{DataDir: dir})

	resp := do(t, "GET", srv.URL+"/api/v1/enterprise/artifacts", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("artifacts = %d", resp.StatusCode)
	}
	var arts struct {
		AppFW []string `json:"AppFW"`
	}
	json.NewDecoder(resp.Body).Decode(&arts)
	resp.Body.Close()
	if len(arts.AppFW) != 1 || arts.AppFW[0] != "r.raw" {
		t.Fatalf("artifacts = %+v", arts)
	}
}
