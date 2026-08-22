# Enterprise Modules Installer — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Historical record.** This plan predates the `advisor` module added 2026-08-23 (see `docs/superpowers/plans/2026-08-23-advisor-enterprise-module.md`). The two-module (`appfw`/`cmp`) scope below is left as originally written; it is no longer the full module set.

**Goal:** Add an "Enterprise Modules" installer to cube-cos-driver that installs App-Framework 2 and Cube-CMP 2.1.0 onto an already-running cluster over SSH-to-VIP, driven by pre-staged offline artifacts, with auto/manual gating and an air-gap enforcement toggle.

**Architecture:** A new `internal/enterprise` package (Manager + Install/Step model + JSON store) drives an ordered step sequence against one control node via a new mockable `internal/clusterssh` client (SSH `Run` + scp `Push`). New `enterpriseHandlers` expose it over REST; a new React page/modal/progress mirror the existing deploy UI. The driver's first live-cluster client.

**Tech Stack:** Go 1.x (`github.com/bigstack-oss/cube-cos-driver`), `golang.org/x/crypto/ssh`, standard `testing` + `httptest`; React + `react-router` v7 + Vitest (`vitest run`) for the SPA.

## Global Constraints

- Air-gapped install is mandatory: artifacts are **pre-staged** on the driver host (no browser upload, no internet at install time). Verbatim: "nothing is fetched from the internet at install time."
- Drive the documented **`hex_cli`** path over SSH only — no new `cube-cos-api` endpoints, no raw `appctl`/`hex_sdk`.
- Keep code comments short; rationale lives in the spec/PR.
- Credentials stored encrypted via the existing `secret.Box` + `.secret-key` (as BMC passwords are).
- Cluster VIP = `model.ClusterDetail.ClusterConfig.HASettings.VirtualIP`; default password `Cube@<last-two-octets-of-VIP>`.
- Mirror existing patterns: `internal/orchestrator/{manager,store,model}.go`, `internal/api/deploy.go`, `web/src/api/deploy.ts`, `web/src/pages/cluster/deploy/DeployModal.tsx`, `web/src/components/AppSidebar.tsx`, `web/src/App.tsx`.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/clusterssh/client.go` | `Client` interface + real SSH/scp impl (`NewSSHClient`) |
| `internal/clusterssh/mock.go` | `MockClient` recording calls, scriptable outputs/errors (test-only helper, exported for enterprise tests) |
| `internal/enterprise/model.go` | `StepState`, `Step`, `InstallParams`, `Install`, module constants |
| `internal/enterprise/artifacts.go` | discover `<DataDir>/enterprise/{appfw,cubecmp}` files |
| `internal/enterprise/plan.go` | `plannedStep` + `BuildPlan(module, params, airgap)` — pure ordering/command logic |
| `internal/enterprise/store.go` | JSON persistence at `<DataDir>/installs/<clusterID>-<module>.json` |
| `internal/enterprise/manager.go` | `Manager`: Start/Status/Next/Cancel; executes plan via `clusterssh.Client`; framework detection; airgap-apply |
| `internal/api/enterprise.go` | `enterpriseHandlers` + `register(mux)` |
| `internal/api/server.go` (modify) | instantiate enterprise store/manager/handlers; ensure data-dir folders |
| `cmd/cube-cos-driver/main.go` (modify) | none required (reuses `--data-dir`) |
| `web/src/api/enterprise.ts` | API client + TS types mirroring the Go shapes |
| `web/src/pages/enterprise/EnterprisePage.tsx` | module cards + Install buttons |
| `web/src/pages/enterprise/InstallModal.tsx` | cluster/creds/params/advanced form → start |
| `web/src/pages/enterprise/InstallProgress.tsx` | streaming step list + completion card |
| `web/src/components/AppSidebar.tsx` (modify) | add "Enterprise Modules" nav item |
| `web/src/App.tsx` (modify) | add `/enterprise` route |

Module scope names: `appfw`, `cmp`. Step names (stable, used by UI + tests): `preflight`, `airgap-apply`, `import_fs`, `import_lb`, `import`, `framework_create`, `app_register`, `complete`.

---

## Task 1: `internal/clusterssh` — SSH/scp client + mock

**Files:**
- Create: `internal/clusterssh/client.go`
- Create: `internal/clusterssh/mock.go`
- Test: `internal/clusterssh/mock_test.go`

**Interfaces — Produces:**
```go
package clusterssh

type Client interface {
    // Run executes cmd on the remote host, calling onLine for each stdout line
    // (onLine may be nil). Returns an error containing captured stderr on non-zero exit.
    Run(ctx context.Context, cmd string, onLine func(string)) error
    // Push copies a local file to remotePath on the remote host (scp-over-SSH).
    Push(ctx context.Context, localPath, remotePath string) error
    Close() error
}
// NewSSHClient dials root@host:22 with password auth (InsecureIgnoreHostKey — the
// VIP is on a trusted mgmt LAN, matching the deploy verifier's ping-only trust).
func NewSSHClient(host, user, password string) (Client, error)
```
`MockClient` (mock.go) records `Runs []string` and `Pushes [][2]string`, and resolves output/error via a `Script func(cmd string) (lines []string, err error)` field (default: no output, nil error).

- [ ] **Step 1: Write failing test** (`mock_test.go`)
```go
func TestMockRecordsAndScripts(t *testing.T) {
    var got []string
    m := &MockClient{Script: func(cmd string) ([]string, error) {
        if strings.Contains(cmd, "framework_list") { return []string{"proj-a", "proj-b"}, nil }
        return nil, nil
    }}
    if err := m.Run(context.Background(), "hex_cli -c app -c framework_list", func(l string){ got = append(got, l) }); err != nil {
        t.Fatal(err)
    }
    if len(got) != 2 || got[0] != "proj-a" { t.Fatalf("lines=%v", got) }
    _ = m.Push(context.Background(), "/a", "/b")
    if len(m.Runs) != 1 || len(m.Pushes) != 1 { t.Fatalf("recorded runs=%v pushes=%v", m.Runs, m.Pushes) }
}
```
- [ ] **Step 2: Run — expect FAIL** `go test ./internal/clusterssh/ -run TestMock` (undefined `MockClient`).
- [ ] **Step 3: Implement `mock.go`** — `MockClient` with `Runs`, `Pushes`, `Script`; `Run` appends cmd to `Runs`, calls `Script`, feeds lines to `onLine`; `Push` appends `[localPath, remotePath]`; `Close` returns nil.
- [ ] **Step 4: Implement `client.go`** — real `sshClient` using `golang.org/x/crypto/ssh`: `Run` opens a session, streams stdout via `bufio.Scanner` on `session.StdoutPipe()`, captures stderr, returns `fmt.Errorf("%s: %w (%s)", cmd, err, stderr)` on failure; `Push` uses an scp sink (`scp -t remotePath`) or `sftp` via `golang.org/x/crypto/ssh` — prefer `github.com/pkg/sftp` if already vendored, else scp protocol. Verify the dependency: `go list -m golang.org/x/crypto` (add via `go get` if absent).
- [ ] **Step 5: Run — expect PASS**; then `go vet ./internal/clusterssh/`.
- [ ] **Step 6: Commit** `git add internal/clusterssh && git commit -m "feat(enterprise): clusterssh Run/Push client + mock"`

---

## Task 2: enterprise model + plan builder

**Files:**
- Create: `internal/enterprise/model.go`, `internal/enterprise/plan.go`
- Test: `internal/enterprise/plan_test.go`

**Interfaces — Produces:**
```go
package enterprise

const (ModuleAppFW = "appfw"; ModuleCMP = "cmp")
type StepState string
const (StepPending StepState="pending"; StepActive="active"; StepDone="done"; StepError="error"; StepSkipped="skipped")

type InstallParams struct {
    Project     string // framework_create project name
    PublicNet   string // default "public"
    MgmtNet     string // default "public"
    LBIP        string
    OSImage     string // rancher .raw basename (no path)
    Framework   string // CMP: target framework (may equal Project when auto-created)
    AppFile     string // CMP: cube-portal-*.pigz basename
    FsImage     string // manila-*.qcow2 basename
    LBImage     string // amphora-*.qcow2 basename
}
type Step struct { Name, Title string; State StepState; Output string; Err string }

// plannedStep is the executable form (not persisted; rebuilt from params).
type plannedStep struct {
    Name, Title, Kind string   // Kind: "run" | "scp+run" | "airgap" | "detect" | "complete"
    Cmd               string   // for run / scp+run
    LocalPath         string   // for scp+run: <DataDir>/enterprise/.../<file>
    RemotePath        string   // for scp+run: cephfs dir
    ImageName         string   // for scp+run image imports: glance image name to idempotency-check
}

// BuildPlan returns the ordered steps for a module. haveFramework=false forces the
// appfw sequence for CMP. airgap prepends the airgap-apply step. dataDir locates artifacts.
func BuildPlan(module string, p InstallParams, airgap, haveFramework bool, dataDir string) []plannedStep
```
Command strings (verbatim from spec §"Step sequences"):
- import_fs: `hex_cli -c iaas -c image -c import_fs local <FsImage>`
- import_lb: `hex_cli -c iaas -c image -c import_lb local <LBImage>`
- import: `hex_cli -c iaas -c image -c import local <OSImage>`
- framework_create: `hex_cli -c app -c framework_create <Project> <PublicNet> <MgmtNet> <LBIP> <os-image-name-no-ext>`
- app_register: `hex_cli -c app -c app_register /mnt/cephfs/update/<AppFile> <Framework> skip_flavor`
- airgap-apply: `cubectl node exec -p 'hex_sdk airgap_sim_apply'`
- image scp RemotePath: `/mnt/cephfs/glance` (import reads from cephfs glance dir); app_register scp RemotePath: `/mnt/cephfs/update`

- [ ] **Step 1: Write failing test** (`plan_test.go`)
```go
func names(ps []plannedStep) []string { var n []string; for _, s := range ps { n = append(n, s.Name) }; return n }

func TestBuildPlan_AppFW(t *testing.T) {
    p := InstallParams{Project:"cmp", PublicNet:"public", MgmtNet:"public", LBIP:"10.32.36.120",
        OSImage:"rancher-cluster-image-rke2-v1.32.4.raw", FsImage:"manila-service-image-yoga.qcow2", LBImage:"amphora-x64-haproxy-yoga.qcow2"}
    got := names(BuildPlan(ModuleAppFW, p, false, false, "/data"))
    want := []string{"preflight","import_fs","import_lb","import","framework_create"}
    if !reflect.DeepEqual(got, want) { t.Fatalf("got %v want %v", got, want) }
}
func TestBuildPlan_AppFW_Airgap(t *testing.T) {
    got := names(BuildPlan(ModuleAppFW, InstallParams{}, true, false, "/data"))
    if got[0] != "preflight" || got[1] != "airgap-apply" { t.Fatalf("airgap not after preflight: %v", got) }
}
func TestBuildPlan_CMP_NoFramework_RunsAppFWFirst(t *testing.T) {
    got := names(BuildPlan(ModuleCMP, InstallParams{}, false, false, "/data"))
    want := []string{"preflight","import_fs","import_lb","import","framework_create","app_register","complete"}
    if !reflect.DeepEqual(got, want) { t.Fatalf("got %v want %v", got, want) }
}
func TestBuildPlan_CMP_HaveFramework_SkipsAppFW(t *testing.T) {
    got := names(BuildPlan(ModuleCMP, InstallParams{Framework:"cmp"}, false, true, "/data"))
    want := []string{"preflight","app_register","complete"}
    if !reflect.DeepEqual(got, want) { t.Fatalf("got %v want %v", got, want) }
}
func TestBuildPlan_FrameworkCreateCmd_UsesImageNameNoExt(t *testing.T) {
    p := InstallParams{Project:"cmp", PublicNet:"public", MgmtNet:"public", LBIP:"10.32.36.120", OSImage:"rancher-cluster-image-rke2-v1.32.4.raw"}
    ps := BuildPlan(ModuleAppFW, p, false, false, "/data")
    var fc plannedStep; for _, s := range ps { if s.Name=="framework_create" { fc=s } }
    if !strings.Contains(fc.Cmd, "rancher-cluster-image-rke2-v1.32.4") || strings.Contains(fc.Cmd, ".raw") {
        t.Fatalf("framework_create cmd = %q", fc.Cmd)
    }
}
```
- [ ] **Step 2: Run — expect FAIL** `go test ./internal/enterprise/ -run TestBuildPlan`
- [ ] **Step 3: Implement `model.go`** (types above) **and `plan.go`** (`BuildPlan`): preflight first; if airgap → append `airgap-apply`; appfw steps when `module==appfw` OR (`module==cmp` && !haveFramework); then for cmp: `app_register` + `complete`. framework_create uses `strings.TrimSuffix(p.OSImage, ".raw")`. Set `LocalPath = filepath.Join(dataDir, "enterprise", sub, file)` and `RemotePath`/`ImageName` per the command table.
- [ ] **Step 4: Run — expect PASS**
- [ ] **Step 5: Commit** `git commit -am "feat(enterprise): install model + BuildPlan step sequencing"`

---

## Task 3: artifact discovery

**Files:** Create `internal/enterprise/artifacts.go`; Test `internal/enterprise/artifacts_test.go`

**Interfaces — Produces:**
```go
type Artifacts struct { AppFW []string; CMP []string } // basenames
func DiscoverArtifacts(dataDir string) Artifacts // scans <dataDir>/enterprise/{appfw,cubecmp}; missing dir → empty slice, never error
```
- [ ] **Step 1: Failing test** — make a temp dir with `enterprise/appfw/rancher-...raw` + `enterprise/cubecmp/cube-portal-2.1.0.pigz`; assert `DiscoverArtifacts` returns those basenames; assert a dataDir with no `enterprise/` returns empty (no panic).
- [ ] **Step 2: Run — FAIL**
- [ ] **Step 3: Implement** using `os.ReadDir`; skip subdirs/dotfiles; sort names.
- [ ] **Step 4: Run — PASS**
- [ ] **Step 5: Commit** `git commit -am "feat(enterprise): discover pre-staged artifacts"`

---

## Task 4: install store

**Files:** Create `internal/enterprise/store.go`; Test `internal/enterprise/store_test.go`
**Interfaces — Consumes:** `Install` (Task 5 defines it — see below). **Produces:**
```go
type Store struct { dir string }
func NewStore(dir string) (*Store, error)               // MkdirAll(dir)
func (s *Store) Save(in *Install) error                 // <dir>/<ClusterID>-<Module>.json (0600)
func (s *Store) Load(clusterID, module string) (*Install, bool)
```
`Install` (define in `model.go`, Task 2/5):
```go
type Install struct {
    ClusterID, Module, StartedAt string
    Manual bool; ManualStep int; SimulateAirgap bool
    Params InstallParams
    Steps []*Step; Current int
    State string // "running" | "done" | "error"
    Portal string
}
```
- [ ] **Step 1: Failing test** — Save an `Install`, Load it back, assert round-trip equality of `ClusterID/Module/State/Steps`; Load of a missing key returns `(nil,false)`.
- [ ] **Step 2: Run — FAIL** · **Step 3: Implement** (mirror `internal/orchestrator/store.go`) · **Step 4: PASS** · **Step 5: Commit** `git commit -am "feat(enterprise): install JSON store"`

---

## Task 5: Manager (core orchestration)

**Files:** Create `internal/enterprise/manager.go`; Test `internal/enterprise/manager_test.go`

**Interfaces — Consumes:** `clusterssh.Client` (Task 1), `BuildPlan`/`InstallParams`/`Install`/`Step` (Tasks 2/4), `DiscoverArtifacts` (Task 3), `Store` (Task 4). **Produces:**
```go
type Manager struct { /* store *Store; dataDir string; dial func(host,user,pw string)(clusterssh.Client,error); mu; installs map[string]*Install; cancels map[string]context.CancelFunc */ }
func NewManager(store *Store, dataDir string, dial func(host,user,pw string)(clusterssh.Client,error)) *Manager

// Start builds the plan (auto-detecting an existing framework for CMP via framework_list),
// persists the Install, and — unless manual — runs steps in a goroutine.
func (m *Manager) Start(clusterID, module, vip, password string, p InstallParams, manual, airgap bool) (*Install, error)
func (m *Manager) Status(clusterID, module string) (*Install, bool)
func (m *Manager) Next(clusterID, module string) error   // manual: run exactly the current step, advance
func (m *Manager) Cancel(clusterID, module string)
```
Execution of a `plannedStep` by Kind:
- `detect`/`preflight`: run `cubectl node exec ... hostname` reachability + `hex_cli -c app -c framework_list`; if module needs a fresh framework and the name already exists → error "framework <name> already exists". Missing artifacts (params reference a basename not in `DiscoverArtifacts`) → error.
- `airgap`: `client.Run(ctx, "cubectl node exec -p 'hex_sdk airgap_sim_apply'", onLine)`.
- `scp+run`: idempotency — first `client.Run("openstack image show <ImageName>")`; on success mark step `skipped`; else `client.Push(LocalPath, RemotePath)` then `client.Run(Cmd)`.
- `run`: `client.Run(Cmd, onLine)` appending lines to `Step.Output`.
- `complete`: set `Portal="http://"+p.LBIP`, `State="done"`.
Any step error → `Step.State=error`, `Step.Err`, `Install.State="error"`, stop. Persist after every step.

- [ ] **Step 1: Write failing tests** (`manager_test.go`) driving a `MockClient` via injected `dial`:
```go
func newTestMgr(t *testing.T, script func(string)([]string,error)) (*Manager, *clusterssh.MockClient) {
    dir := t.TempDir()
    // stage artifacts referenced by params
    os.MkdirAll(filepath.Join(dir,"enterprise","appfw"),0755); os.WriteFile(filepath.Join(dir,"enterprise","appfw","r.raw"),nil,0644)
    st,_ := NewStore(filepath.Join(dir,"installs"))
    mc := &clusterssh.MockClient{Script: script}
    return NewManager(st, dir, func(h,u,p string)(clusterssh.Client,error){return mc,nil}), mc
}

func TestManager_AppFW_AutoRunsAllStepsInOrder(t *testing.T) {
    m,mc := newTestMgr(t, func(cmd string)([]string,error){ return nil,nil })
    in,err := m.Start("cl1","appfw","10.32.10.140","pw",
        InstallParams{Project:"cmp",PublicNet:"public",MgmtNet:"public",LBIP:"10.32.36.120",OSImage:"r.raw",FsImage:"m.qcow2",LBImage:"a.qcow2"}, false, false)
    if err!=nil { t.Fatal(err) }
    waitState(t,m,"cl1","appfw","done")
    // framework_create issued with image name (no .raw)
    if !containsCmd(mc.Runs,"framework_create cmp public public 10.32.36.120 r") { t.Fatalf("runs=%v", mc.Runs) }
    _ = in
}
func TestManager_CMP_NoFramework_RunsAppFWThenRegister(t *testing.T) {
    m,mc := newTestMgr(t, func(cmd string)([]string,error){
        if strings.Contains(cmd,"framework_list") { return nil,nil } // no existing framework
        return nil,nil })
    m.Start("cl1","cmp","10.32.10.140","pw", InstallParams{Project:"cmp",LBIP:"10.32.36.120",OSImage:"r.raw",AppFile:"cube-portal-2.1.0.pigz"}, false, false)
    waitState(t,m,"cl1","cmp","done")
    if !containsCmd(mc.Runs,"framework_create") || !containsCmd(mc.Runs,"app_register /mnt/cephfs/update/cube-portal-2.1.0.pigz") {
        t.Fatalf("runs=%v", mc.Runs)
    }
}
func TestManager_CMP_ExistingFramework_SkipsAppFW(t *testing.T) {
    m,mc := newTestMgr(t, func(cmd string)([]string,error){
        if strings.Contains(cmd,"framework_list") { return []string{"cmp"},nil } // exists
        return nil,nil })
    m.Start("cl1","cmp","10.32.10.140","pw", InstallParams{Framework:"cmp",AppFile:"cube-portal-2.1.0.pigz",LBIP:"10.32.36.120"}, false, false)
    waitState(t,m,"cl1","cmp","done")
    if containsCmd(mc.Runs,"framework_create") { t.Fatalf("should not create framework: %v", mc.Runs) }
}
func TestManager_Manual_NextAdvancesOneStep(t *testing.T) {
    m,_ := newTestMgr(t, func(cmd string)([]string,error){return nil,nil})
    m.Start("cl1","appfw","10.32.10.140","pw", validAppFWParams(), true /*manual*/, false)
    in,_ := m.Status("cl1","appfw"); if in.Current!=0 { t.Fatal("should start at 0") }
    m.Next("cl1","appfw"); in,_ = m.Status("cl1","appfw"); if in.Current!=1 { t.Fatalf("current=%d", in.Current) }
}
func TestManager_ImportSkippedWhenImageExists(t *testing.T) {
    m,mc := newTestMgr(t, func(cmd string)([]string,error){
        if strings.Contains(cmd,"image show") { return []string{"exists"},nil } // present
        return nil,nil })
    m.Start("cl1","appfw","10.32.10.140","pw", validAppFWParams(), false, false)
    waitState(t,m,"cl1","appfw","done")
    if len(mc.Pushes)!=0 { t.Fatalf("should skip scp when image exists: %v", mc.Pushes) }
    in,_:=m.Status("cl1","appfw"); if stepState(in,"import")!="skipped" { t.Fatal("import not skipped") }
}
func TestManager_Airgap_AppliedBeforeInstallSteps(t *testing.T) {
    m,mc := newTestMgr(t, func(cmd string)([]string,error){return nil,nil})
    m.Start("cl1","appfw","10.32.10.140","pw", validAppFWParams(), false, true /*airgap*/)
    waitState(t,m,"cl1","appfw","done")
    ai := indexOfCmd(mc.Runs,"airgap_sim_apply"); ii := indexOfCmd(mc.Runs,"framework_create")
    if ai<0 || ai>ii { t.Fatalf("airgap not before install: %v", mc.Runs) }
}
func TestManager_StepFailure_StopsAndErrors(t *testing.T) {
    m,_ := newTestMgr(t, func(cmd string)([]string,error){
        if strings.Contains(cmd,"framework_create") { return nil, errors.New("boom") }
        return nil,nil })
    m.Start("cl1","appfw","10.32.10.140","pw", validAppFWParams(), false, false)
    waitState(t,m,"cl1","appfw","error")
    in,_:=m.Status("cl1","appfw"); if stepState(in,"framework_create")!="error" { t.Fatal("framework_create not errored") }
}
```
(Provide the small helpers `waitState`, `containsCmd`, `indexOfCmd`, `stepState`, `validAppFWParams` in the test file — poll `Status` up to ~2s for async runs.)
- [ ] **Step 2: Run — expect FAIL**
- [ ] **Step 3: Implement `manager.go`** per the execution rules above; `Start` dials via `m.dial(vip,"root",password)`, runs `framework_list` to compute `haveFramework`, calls `BuildPlan`, seeds `Install.Steps` from plan names/titles, persists, and (auto) launches `runAll` goroutine; manual leaves `Current=0` for `Next`. Guard `installs`/`cancels` with a mutex.
- [ ] **Step 4: Run — expect PASS**; `go vet ./internal/enterprise/`
- [ ] **Step 5: Commit** `git commit -am "feat(enterprise): install Manager — sequencing, framework detect, airgap, idempotent import, manual gating"`

---

## Task 6: REST handlers + server wiring

**Files:** Create `internal/api/enterprise.go`; Modify `internal/api/server.go`; Test `internal/api/enterprise_test.go`

**Interfaces — Consumes:** `enterprise.Manager`, `storage.Store` (for VIP via `Detail`), `secret.Box`. **Produces routes** (mirror `deploy.go` `register`):
- `GET  /api/v1/enterprise/artifacts` → `enterprise.DiscoverArtifacts(dataDir)`
- `POST /api/v1/clusters/{id}/enterprise/install` — body `{module, params, manual, simulateAirgap, password}`; resolve VIP from `clusters.Detail(id).ClusterConfig.HASettings.VirtualIP`; default password `Cube@<last2octets(vip)>` when blank; encrypt+persist password via `secret.Box`; call `mgr.Start`
- `GET  /api/v1/clusters/{id}/enterprise/install?module=` → `mgr.Status`
- `POST /api/v1/clusters/{id}/enterprise/install/step/next?module=` → `mgr.Next`
- `POST /api/v1/clusters/{id}/enterprise/install/cancel?module=` → `mgr.Cancel`

**server.go** (near line 80–125): add
```go
entStore, err := enterprise.NewStore(filepath.Join(cfg.DataDir, "installs"))
if err != nil { return nil, nil, err }
entMgr := enterprise.NewManager(entStore, cfg.DataDir, func(h,u,p string)(clusterssh.Client,error){ return clusterssh.NewSSHClient(h,u,p) })
eh := &enterpriseHandlers{clusters: clusterStore, mgr: entMgr, dataDir: cfg.DataDir}
eh.register(mux)
```

- [ ] **Step 1: Failing test** (`enterprise_test.go`) via `httptest`: seed a `storage.Store` cluster with `HASettings.VirtualIP="10.32.10.140"`; POST an install with `{module:"appfw", manual:true, params:{...}}`; assert 202 + body; GET status returns the seeded steps; use an injected mock dial (add a small test seam: `newHandlerWithDial`) so no real SSH.
- [ ] **Step 2: Run — FAIL** · **Step 3: Implement** handlers + server wiring (default-password helper: `"Cube@"+strings.Join(strings.Split(vip,".")[2:],".")`) · **Step 4: PASS**; `go build ./...` · **Step 5: Commit** `git commit -am "feat(enterprise): REST handlers + server wiring"`

---

## Task 7: frontend API client

**Files:** Create `web/src/api/enterprise.ts`; Test `web/src/api/enterprise.test.ts`
**Produces** (mirror `web/src/api/deploy.ts`): TS types `Artifacts`, `InstallParams`, `Step`, `Install`, and fns `getArtifacts()`, `startInstall(id, body)`, `getInstall(id, module)`, `nextStep(id, module)`, `cancelInstall(id, module)`.
- [ ] **Step 1: Failing test** — mock `fetch`; assert `startInstall` POSTs to `/api/v1/clusters/ID/enterprise/install` with the JSON body; `getInstall` GETs `?module=`.
- [ ] **Step 2: FAIL** · **Step 3: Implement** · **Step 4: `pnpm -C web test` PASS** · **Step 5: Commit** `git commit -am "feat(enterprise): web API client"`

---

## Task 8: Enterprise page + nav/route

**Files:** Create `web/src/pages/enterprise/EnterprisePage.tsx`; Modify `web/src/components/AppSidebar.tsx`, `web/src/App.tsx`; Test `web/src/pages/enterprise/EnterprisePage.test.tsx`
**Details:** Add `{ path:'/enterprise', label:'Enterprise Modules' }` to `navItems`; add `<Route path="/enterprise" element={<EnterprisePage/>}/>`. Page lists two cards (App-Framework, Cube-CMP) each with an **Install** button opening `InstallModal` with the chosen module.
- [ ] **Step 1: Failing test** — render `EnterprisePage`, assert both module cards + Install buttons present.
- [ ] **Step 2: FAIL** · **Step 3: Implement** · **Step 4: PASS** · **Step 5: Commit** `git commit -am "feat(enterprise): page + nav/route"`

---

## Task 9: InstallModal

**Files:** Create `web/src/pages/enterprise/InstallModal.tsx`; Test alongside.
**Details (mirror `DeployModal.tsx`):** cluster dropdown (from `GET /api/v1/clusters`); password field showing the default `Cube@<last2>` placeholder; `framework_create` fields (project, public net `public`, mgmt net `public`, LB IP, OS image picker from `appfw/`); **Advanced** (collapsed `<details>`): auto/manual checkbox, air-gap toggle with the `cubectl node exec -p 'hex_sdk airgap_sim_clear'` note; for CMP add framework picker + `.pigz` picker from `cubecmp/`. Submit → `startInstall`.
- [ ] **Step 1: Failing test** — render modal for `module="cmp"`, assert framework + `.pigz` pickers present and that submit calls `startInstall` with the params.
- [ ] **Step 2: FAIL** · **Step 3: Implement** · **Step 4: PASS** · **Step 5: Commit** `git commit -am "feat(enterprise): install modal"`

---

## Task 10: InstallProgress + completion card

**Files:** Create `web/src/pages/enterprise/InstallProgress.tsx`; Test alongside.
**Details (mirror `DeployProgress.tsx`):** poll `getInstall`; render the step list with per-step state + streamed `Output`; manual **Next** button gated on the current step; on `State==="done"` show the **Next steps** card — `✅ <module> installed`, clickable `http://<lb-ip>` portal link (new tab), the "set admin permission" instruction + runbook link.
- [ ] **Step 1: Failing test** — given an `Install` with `State:"done", Portal:"http://10.32.36.120"`, assert the portal link renders with that href; given a `manual` running install, assert the **Next** button calls `nextStep`.
- [ ] **Step 2: FAIL** · **Step 3: Implement** · **Step 4: `pnpm -C web test` + `pnpm -C web build` PASS** · **Step 5: Commit** `git commit -am "feat(enterprise): install progress + completion card"`

---

## Final integration

- [ ] `go build ./... && go test ./...` green; `pnpm -C web test && pnpm -C web build` green.
- [ ] Manual smoke (optional, lab): stage artifacts under `<DATA_DIR>/enterprise/`, run an install against a cluster VIP, confirm streaming + completion card.
- [ ] Open a draft PR (link the design spec + a new tracking issue), follow the repo PR flow.

## Notes / risks for the implementer
- The `hex_cli … import local <file>` non-interactive arg form and the exact cephfs glance dir (`CEPHFS_GLANCE_DIR`) should be confirmed against a live cluster before the lab smoke; the plan uses `/mnt/cephfs/glance` and the documented `import local <file>` form.
- `scp` vs `sftp` in `clusterssh.Push`: prefer `sftp` if `github.com/pkg/sftp` is available (simpler, robust); otherwise implement the scp sink protocol. Confirm in Task 1 Step 4.
- The uncommitted `simulateAirgap` driver work on this branch is unrelated to these files — keep it out of enterprise commits (this plan touches new files + `server.go`/`AppSidebar.tsx`/`App.tsx` only).
