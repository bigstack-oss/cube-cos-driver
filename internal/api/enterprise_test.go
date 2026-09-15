package api

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
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
	// Only the two framework-name fields: the tests below assert that each is
	// filled from the other, not the whole param set.
	Params struct {
		Project   string `json:"Project"`
		Framework string `json:"Framework"`
	} `json:"Params"`
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
		[]byte(`{"module":"appfw","manual":true,"params":{"Project":"appfw","OSImage":"rancher.raw"}}`))
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

// cmp's plan imports the portal chart keyed on AppFile; a missing value must
// be rejected before mgr.Start for the same reason OSImage is.
func TestEnterpriseInstallStartRequiresAppFile(t *testing.T) {
	srv, id, _ := enterpriseFixture(t)

	resp := do(t, "POST", srv.URL+"/api/v1/clusters/"+id+"/enterprise/install",
		[]byte(`{"module":"cmp","manual":true,"params":{"Project":"appfw","OSImage":"r.raw","FsImage":"m.qcow2","LBImage":"a.qcow2"}}`))
	if resp.StatusCode != 400 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("start without AppFile = %d, want 400: %s", resp.StatusCode, b)
	}
	resp.Body.Close()
}

// advisor's plan imports its chart keyed on AdvisorFile; a missing value must
// be rejected before mgr.Start for the same reason OSImage is.
func TestEnterpriseInstallStartRequiresAdvisorFile(t *testing.T) {
	srv, id, _ := enterpriseFixture(t)

	resp := do(t, "POST", srv.URL+"/api/v1/clusters/"+id+"/enterprise/install",
		[]byte(`{"module":"advisor","manual":true,"params":{"Project":"appfw","OSImage":"r.raw","FsImage":"m.qcow2","LBImage":"a.qcow2","AdvisorLBIP":"10.0.0.9"}}`))
	if resp.StatusCode != 400 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("start without AdvisorFile = %d, want 400: %s", resp.StatusCode, b)
	}
	resp.Body.Close()
}

// advisor's install step is keyed on AdvisorLBIP; a missing value must be
// rejected before mgr.Start — otherwise the run dies mid-plan after minutes
// of imports instead of failing fast.
func TestEnterpriseInstallStartRequiresAdvisorLBIP(t *testing.T) {
	srv, id, _ := enterpriseFixture(t)

	resp := do(t, "POST", srv.URL+"/api/v1/clusters/"+id+"/enterprise/install",
		[]byte(`{"module":"advisor","manual":true,"params":{"Project":"appfw","OSImage":"r.raw","FsImage":"m.qcow2","LBImage":"a.qcow2","AdvisorFile":"cube-advisor-1.2.3.pigz"}}`))
	if resp.StatusCode != 400 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("start without AdvisorLBIP = %d, want 400: %s", resp.StatusCode, b)
	}
	resp.Body.Close()
}

// A framework name is required, and reaches the plan through two fields --
// framework_create reads Project, advisor_register reads Framework. A request
// carrying neither must be refused here: downstream nothing catches it, and
// the run dies at the cluster as "invalid arguments" with an empty step output
// that never mentions the blank name.
func TestEnterpriseInstallStartRequiresFrameworkName(t *testing.T) {
	srv, id, _ := enterpriseFixture(t)

	resp := do(t, "POST", srv.URL+"/api/v1/clusters/"+id+"/enterprise/install",
		[]byte(`{"module":"advisor","manual":true,"params":{"OSImage":"r.raw","AdvisorFile":"cube-advisor-1.2.3.pigz","AdvisorLBIP":"10.0.0.9"}}`))
	if resp.StatusCode != 400 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("start without a framework name = %d, want 400: %s", resp.StatusCode, b)
	}
	resp.Body.Close()
}

// Framework alone is enough: it is the field the CMP and advisor register
// steps read, and a caller that sets only it means the same framework
// framework_create would be given.
func TestEnterpriseInstallStartFrameworkFillsProject(t *testing.T) {
	srv, id, _ := enterpriseFixture(t)

	resp := do(t, "POST", srv.URL+"/api/v1/clusters/"+id+"/enterprise/install",
		[]byte(`{"module":"advisor","manual":true,"params":{"Framework":"appfw","OSImage":"r.raw","AdvisorFile":"cube-advisor-1.2.3.pigz","AdvisorLBIP":"10.0.0.9"}}`))
	if resp.StatusCode != 202 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("start with only Framework = %d, want 202: %s", resp.StatusCode, b)
	}
	var in enterpriseInstall
	json.NewDecoder(resp.Body).Decode(&in)
	resp.Body.Close()
	if in.Params.Project != "appfw" {
		t.Fatalf("Project = %q, want it filled from Framework", in.Params.Project)
	}
}

// And the other way round: Project alone fills Framework, which is what the
// UI sends and what every advisor install before this fix relied on.
func TestEnterpriseInstallStartProjectFillsFramework(t *testing.T) {
	srv, id, _ := enterpriseFixture(t)

	resp := do(t, "POST", srv.URL+"/api/v1/clusters/"+id+"/enterprise/install",
		[]byte(`{"module":"advisor","manual":true,"params":{"Project":"appfw","OSImage":"r.raw","AdvisorFile":"cube-advisor-1.2.3.pigz","AdvisorLBIP":"10.0.0.9"}}`))
	if resp.StatusCode != 202 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("start with only Project = %d, want 202: %s", resp.StatusCode, b)
	}
	var in enterpriseInstall
	json.NewDecoder(resp.Body).Decode(&in)
	resp.Body.Close()
	if in.Params.Framework != "appfw" {
		t.Fatalf("Framework = %q, want it filled from Project", in.Params.Framework)
	}
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

// The advisor's console pool is probed by Introspect and has to survive the
// response struct. It did not: cluster-info serialises an inline struct, and a
// field added to ClusterQuery alone is computed and then dropped, so the
// install form could never offer a pool and no caller could learn one without
// running the probe itself.
func TestClusterInfoReturnsTheAdvisorPool(t *testing.T) {
	srv, id, _ := enterpriseFixture(t)

	resp := do(t, "POST", srv.URL+"/api/v1/clusters/"+id+"/enterprise/cluster-info", []byte(`{}`))
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("cluster-info = %d, want 200: %s", resp.StatusCode, b)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["suggestedAdvisorPool"]; !ok {
		t.Fatalf("cluster-info has no suggestedAdvisorPool key; keys=%v", keysOf(body))
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
