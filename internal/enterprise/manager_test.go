package enterprise

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bigstack-oss/cube-cos-driver/internal/clusterssh"
)

// frameworkActiveAfterCreate returns a Script that reports framework `name` as
// active once framework_create has been issued — modelling real provisioning so
// the framework poll completes. extra handles any other command (nil = no-op).
func frameworkActiveAfterCreate(name string, extra func(string) ([]string, error)) func(string) ([]string, error) {
	var mu sync.Mutex
	created := false
	return func(cmd string) ([]string, error) {
		mu.Lock()
		if strings.Contains(cmd, "framework_create") {
			created = true
		}
		active := created
		mu.Unlock()
		if strings.Contains(cmd, "framework_list") {
			if active {
				return []string{"c-m-1  " + name + "  rancher  3  active  2026-01-01"}, nil
			}
			return nil, nil
		}
		if strings.Contains(cmd, "image show amphora-x64-haproxy") {
			return []string{"['amphora']"}, nil // already tagged
		}
		if strings.Contains(cmd, "systeminfo") {
			return []string{"200"}, nil // harbor registry reachable
		}
		if extra != nil {
			return extra(cmd)
		}
		return nil, nil
	}
}

func newTestMgr(t *testing.T, script func(string) ([]string, error)) (*Manager, *clusterssh.MockClient) {
	t.Helper()
	dir := t.TempDir()
	// stage artifacts referenced by params
	appfw := filepath.Join(dir, "enterprise", "appfw")
	os.MkdirAll(appfw, 0o755)
	for _, f := range []string{"r.raw", "m.qcow2", "a.qcow2"} {
		os.WriteFile(filepath.Join(appfw, f), []byte("x"), 0o644)
	}
	cmp := filepath.Join(dir, "enterprise", "cubecmp")
	os.MkdirAll(cmp, 0o755)
	os.WriteFile(filepath.Join(cmp, "cube-portal-2.1.0.pigz"), []byte("x"), 0o644)
	advisor := filepath.Join(dir, "enterprise", "advisor")
	os.MkdirAll(advisor, 0o755)
	os.WriteFile(filepath.Join(advisor, "cube-advisor-1.2.3.pigz"), []byte("x"), 0o644)
	st, _ := NewStore(filepath.Join(dir, "installs"))
	inner := script
	// default /etc/version answer so cmp/advisor preflight can read the cluster version
	script = func(cmd string) ([]string, error) {
		var out []string
		var err error
		if inner != nil {
			out, err = inner(cmd)
		}
		if strings.Contains(cmd, "cat /etc/version") && err == nil && len(out) == 0 {
			return []string{testClusterVersion}, nil
		}
		return out, err
	}
	mc := &clusterssh.MockClient{Script: script}
	return NewManager(st, NewDir(dir, filepath.Join(dir, "enterprise")), func(h, u, p string) (clusterssh.Client, error) { return mc, nil }), mc
}

func TestManager_AppFW_AutoRunsAllStepsInOrder(t *testing.T) {
	m, mc := newTestMgr(t, frameworkActiveAfterCreate("cmp", nil))
	in, err := m.Start("cl1", "appfw", "10.32.10.140", "pw",
		InstallParams{Project: "cmp", PublicNet: "public", MgmtNet: "public", LBIP: "10.32.36.120", OSImage: "r.raw", FsImage: "m.qcow2", LBImage: "a.qcow2"}, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "cl1", "appfw", "done")
	// framework_create issued with image name (no .raw)
	if !containsCmd(mc.Runs, "framework_create cmp public public 10.32.36.120 r") {
		t.Fatalf("runs=%v", mc.Runs)
	}
	_ = in
}

func TestManager_CMP_NoFramework_RunsAppFWThenRegister(t *testing.T) {
	m, mc := newTestMgr(t, frameworkActiveAfterCreate("cmp", nil))
	m.Start("cl1", "cmp", "10.32.10.140", "pw", InstallParams{Project: "cmp", LBIP: "10.32.36.120", OSImage: "r.raw", AppFile: "cube-portal-2.1.0.pigz"}, false, false, nil)
	waitState(t, m, "cl1", "cmp", "done")
	if !containsCmd(mc.Runs, "framework_create") || !containsCmd(mc.Runs, "app_register /mnt/cephfs/update/cube-portal-2.1.0.pigz") {
		t.Fatalf("runs=%v", mc.Runs)
	}
}

// An already-active framework is skipped (not recreated), but app_register still runs.
func TestManager_CMP_ExistingActiveFramework_SkipsCreate(t *testing.T) {
	m, mc := newTestMgr(t, func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "framework_list") {
			return []string{"c-m-1  cmp  rancher  3  active  2026-01-01"}, nil
		}
		if strings.Contains(cmd, "image show amphora-x64-haproxy") {
			return []string{"['amphora']"}, nil
		}
		if strings.Contains(cmd, "systeminfo") {
			return []string{"200"}, nil
		}
		return nil, nil
	})
	m.Start("cl1", "cmp", "10.32.10.140", "pw", InstallParams{Project: "cmp", Framework: "cmp", AppFile: "cube-portal-2.1.0.pigz", LBIP: "10.32.36.120"}, false, false, nil)
	waitState(t, m, "cl1", "cmp", "done")
	if containsCmd(mc.Runs, "framework_create cmp") {
		t.Fatalf("should not recreate an active framework: %v", mc.Runs)
	}
	in, _ := m.Status("cl1", "cmp")
	// No OS image was given: the plan installs onto the framework that is
	// there and carries no create step at all.
	if stepState(in, "framework_create") != "" {
		t.Fatalf("framework_create should not be planned, got %s", stepState(in, "framework_create"))
	}
	if !containsCmd(mc.Runs, "app_register") {
		t.Fatalf("app_register should still run: %v", mc.Runs)
	}
}

// An already-active framework is skipped (not recreated), but advisor_register
// and install_advisor still run, and the completion Portal URL points at the
// dedicated Advisor LB IP.
func TestManager_Advisor_ExistingActiveFramework_SkipsCreate(t *testing.T) {
	m, mc := newTestMgr(t, func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "framework_list") {
			return []string{"c-m-1  appfw  rancher  3  active  2026-01-01"}, nil
		}
		if strings.Contains(cmd, "image show amphora-x64-haproxy") {
			return []string{"['amphora']"}, nil
		}
		if strings.Contains(cmd, "systeminfo") {
			return []string{"200"}, nil
		}
		return nil, nil
	})
	m.Start("cl1", "advisor", "10.32.10.140", "pw", InstallParams{Project: "appfw", Framework: "appfw",
		AdvisorFile: "cube-advisor-1.2.3.pigz", AdvisorLBIP: "10.0.0.9", LBIP: "10.32.36.120"}, false, false, nil)
	waitState(t, m, "cl1", "advisor", "done")
	if containsCmd(mc.Runs, "framework_create appfw") {
		t.Fatalf("should not recreate an active framework: %v", mc.Runs)
	}
	in, _ := m.Status("cl1", "advisor")
	if stepState(in, "framework_create") != "" {
		t.Fatalf("framework_create should not be planned, got %s", stepState(in, "framework_create"))
	}
	if stepState(in, "advisor_register") != "done" {
		t.Fatalf("advisor_register should be done, got %s", stepState(in, "advisor_register"))
	}
	if stepState(in, "install_advisor") != "done" {
		t.Fatalf("install_advisor should be done, got %s", stepState(in, "install_advisor"))
	}
	if in.Portal != "http://10.0.0.9/" {
		t.Fatalf("Portal = %q, want http://10.0.0.9/", in.Portal)
	}
}

// Uninstalling appfw (framework_delete removes every app on it) must also drop
// any advisor install record for the same cluster/host — advisor runs on the
// framework and would otherwise leave a stale "done" record behind.
func TestManager_AppFWUninstall_CascadesAdvisor(t *testing.T) {
	m, _ := newTestMgr(t, frameworkActiveAfterCreate("appfw", nil))
	m.Start("cl1", "advisor", "10.32.10.140", "pw", InstallParams{Project: "appfw", Framework: "appfw", OSImage: "r.raw",
		AdvisorFile: "cube-advisor-1.2.3.pigz", AdvisorLBIP: "10.0.0.9", LBIP: "10.32.36.120"}, false, false, nil)
	waitState(t, m, "cl1", "advisor", "done")

	m.StartUninstall("cl1", "appfw", "10.32.10.140", "pw", InstallParams{Project: "appfw"}, false)
	waitState(t, m, "cl1", "appfw", "done")

	if _, ok := m.Status("cl1", "advisor"); ok {
		t.Fatal("advisor install record should be dropped after appfw uninstall")
	}
}

func TestManager_Manual_NextAdvancesOneStep(t *testing.T) {
	m, _ := newTestMgr(t, func(cmd string) ([]string, error) { return nil, nil })
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), true /*manual*/, false, nil)
	in, _ := m.Status("cl1", "appfw")
	if in.Current != 0 {
		t.Fatal("should start at 0")
	}
	m.Next("cl1", "appfw")
	in, _ = m.Status("cl1", "appfw")
	if in.Current != 1 {
		t.Fatalf("current=%d", in.Current)
	}
}

func TestManager_ImportSkippedWhenImageExists(t *testing.T) {
	m, mc := newTestMgr(t, frameworkActiveAfterCreate("cmp", func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "image show") {
			return []string{"exists"}, nil
		} // present
		return nil, nil
	}))
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false, nil)
	waitState(t, m, "cl1", "appfw", "done")
	if len(mc.Pushes) != 0 {
		t.Fatalf("should skip scp when image exists: %v", mc.Pushes)
	}
	in, _ := m.Status("cl1", "appfw")
	if stepState(in, "import") != "skipped" {
		t.Fatal("import not skipped")
	}
}

func TestManager_Airgap_AppliedBeforeInstallSteps(t *testing.T) {
	m, mc := newTestMgr(t, frameworkActiveAfterCreate("cmp", nil))
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, true /*airgap*/, nil)
	waitState(t, m, "cl1", "appfw", "done")
	ai := indexOfCmd(mc.Runs, "airgap_sim_apply")
	ii := indexOfCmd(mc.Runs, "framework_create")
	if ai < 0 || ai > ii {
		t.Fatalf("airgap not before install: %v", mc.Runs)
	}
}

func TestManager_StepFailure_StopsAndErrors(t *testing.T) {
	m, _ := newTestMgr(t, func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "framework_create") {
			return nil, errors.New("boom")
		}
		return nil, nil
	})
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false, nil)
	waitState(t, m, "cl1", "appfw", "error")
	in, _ := m.Status("cl1", "appfw")
	if stepState(in, "framework_create") != "error" {
		t.Fatal("framework_create not errored")
	}
}

// md5("hello") = 5d41402abc4b2a76b9719d911017c592
func stageDatadir(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	appfw := filepath.Join(dir, "enterprise", "appfw")
	os.MkdirAll(appfw, 0o755)
	for _, f := range []string{"r.raw", "m.qcow2", "a.qcow2"} {
		os.WriteFile(filepath.Join(appfw, f), []byte(content), 0o644)
	}
	cmp := filepath.Join(dir, "enterprise", "cubecmp")
	os.MkdirAll(cmp, 0o755)
	os.WriteFile(filepath.Join(cmp, "cube-portal-2.1.0.pigz"), []byte(content), 0o644)
	return dir
}

func TestPreflight_MD5Mismatch_Fails(t *testing.T) {
	dir := stageDatadir(t, "hello")
	// wrong md5 sidecar for the rancher image → preflight must reject it
	os.WriteFile(filepath.Join(dir, "enterprise", "appfw", "r.raw.md5"),
		[]byte("deadbeefdeadbeefdeadbeefdeadbeef\n"), 0o644)
	st, _ := NewStore(filepath.Join(dir, "installs"))
	mc := &clusterssh.MockClient{Script: frameworkActiveAfterCreate("cmp", nil)}
	m := NewManager(st, NewDir(dir, filepath.Join(dir, "enterprise")), func(h, u, p string) (clusterssh.Client, error) { return mc, nil })
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false, nil)
	waitState(t, m, "cl1", "appfw", "error")
	in, _ := m.Status("cl1", "appfw")
	if stepState(in, "preflight") != "error" {
		t.Fatalf("preflight should fail on md5 mismatch, got %s", stepState(in, "preflight"))
	}
	if !strings.Contains(stepErr(in, "preflight"), "integrity check") {
		t.Fatalf("expected integrity error, got %q", stepErr(in, "preflight"))
	}
}

func TestPreflight_MD5Match_Passes(t *testing.T) {
	dir := stageDatadir(t, "hello")
	for _, f := range []string{"r.raw", "m.qcow2", "a.qcow2"} {
		os.WriteFile(filepath.Join(dir, "enterprise", "appfw", f+".md5"),
			[]byte("5d41402abc4b2a76b9719d911017c592\n"), 0o644)
	}
	st, _ := NewStore(filepath.Join(dir, "installs"))
	mc := &clusterssh.MockClient{Script: frameworkActiveAfterCreate("cmp", nil)}
	m := NewManager(st, NewDir(dir, filepath.Join(dir, "enterprise")), func(h, u, p string) (clusterssh.Client, error) { return mc, nil })
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false, nil)
	waitState(t, m, "cl1", "appfw", "done")
}

// An untagged amphora image gets tagged before the framework is created.
func TestManager_Framework_TagsAmphoraImage(t *testing.T) {
	m, mc := newTestMgr(t, func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "image show amphora-x64-haproxy") {
			return []string{"[]"}, nil // NOT tagged
		}
		if strings.Contains(cmd, "framework_list") {
			return []string{"c-m-1  cmp  rancher  3  active  2026-01-01"}, nil
		}
		if strings.Contains(cmd, "systeminfo") {
			return []string{"200"}, nil
		}
		return nil, nil
	})
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false, nil)
	waitState(t, m, "cl1", "appfw", "done")
	if !containsCmd(mc.Runs, "image set --tag amphora amphora-x64-haproxy") {
		t.Fatalf("should tag the amphora image when untagged: %v", mc.Runs)
	}
}

// If the framework's Harbor registry isn't reachable, the framework step fails
// (surfacing the registry-setup cascade) rather than proceeding to app_register.
func TestManager_Framework_FailsWhenRegistryUnreachable(t *testing.T) {
	m, _ := newTestMgr(t, func(cmd string) ([]string, error) {
		var created bool
		_ = created
		if strings.Contains(cmd, "image show amphora-x64-haproxy") {
			return []string{"['amphora']"}, nil
		}
		if strings.Contains(cmd, "framework_list") {
			return []string{"c-m-1  cmp  rancher  3  active  2026-01-01"}, nil
		}
		if strings.Contains(cmd, "systeminfo") {
			return []string{"000"}, nil // harbor UNREACHABLE
		}
		return nil, nil
	})
	defer func(to, iv time.Duration) { registryReadyTimeout, registryPollInterval = to, iv }(registryReadyTimeout, registryPollInterval)
	registryReadyTimeout, registryPollInterval = 20*time.Millisecond, 2*time.Millisecond
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false, nil)
	waitState(t, m, "cl1", "appfw", "error")
	in, _ := m.Status("cl1", "appfw")
	if stepState(in, "framework_create") != "error" {
		t.Fatalf("framework_create should error on unreachable registry, got %s", stepState(in, "framework_create"))
	}
	if !strings.Contains(stepErr(in, "framework_create"), "registry") {
		t.Fatalf("expected registry-reachability error, got %q", stepErr(in, "framework_create"))
	}
}

func TestManager_Framework_TimeoutWarns(t *testing.T) {
	// Framework never reaches active → the step fails with the timeout warning.
	defer func(to, iv time.Duration) { frameworkReadyTimeout, frameworkPollInterval = to, iv }(frameworkReadyTimeout, frameworkPollInterval)
	frameworkReadyTimeout, frameworkPollInterval = 20*time.Millisecond, 2*time.Millisecond
	m, _ := newTestMgr(t, func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "framework_list") {
			return []string{"c-m-1  cmp  rancher  0  updating  2026-01-01"}, nil
		}
		return nil, nil
	})
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false, nil)
	waitState(t, m, "cl1", "appfw", "error")
	in, _ := m.Status("cl1", "appfw")
	if stepState(in, "framework_create") != "error" {
		t.Fatalf("framework_create should error on timeout, got %s", stepState(in, "framework_create"))
	}
	if !strings.Contains(stepErr(in, "framework_create"), "not active after") {
		t.Fatalf("expected timeout warning, got %q", stepErr(in, "framework_create"))
	}
}

func TestManager_Start_ConcurrentSameKey_RejectsSecond(t *testing.T) {
	dir := t.TempDir()
	appfw := filepath.Join(dir, "enterprise", "appfw")
	os.MkdirAll(appfw, 0o755)
	for _, f := range []string{"r.raw", "m.qcow2", "a.qcow2"} {
		os.WriteFile(filepath.Join(appfw, f), []byte("x"), 0o644)
	}
	st, _ := NewStore(filepath.Join(dir, "installs"))
	mc := &clusterssh.MockClient{Script: frameworkActiveAfterCreate("cmp", nil)}

	// The winner blocks in dial (reservation already held), guaranteeing the second
	// Start is attempted while the first is mid-flight.
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var dials int32
	m := NewManager(st, NewDir(dir, filepath.Join(dir, "enterprise")), func(h, u, p string) (clusterssh.Client, error) {
		atomic.AddInt32(&dials, 1)
		entered <- struct{}{}
		<-release
		return mc, nil
	})

	results := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false, nil)
			results[i] = err
		}(i)
	}

	<-entered                         // winner has reserved and is blocked in dial
	time.Sleep(50 * time.Millisecond) // let the loser attempt and be rejected
	close(release)
	wg.Wait()

	nilCount := 0
	for _, e := range results {
		if e == nil {
			nilCount++
		}
	}
	if nilCount != 1 {
		t.Fatalf("want exactly one Start to succeed, got %d (results=%v)", nilCount, results)
	}
	if got := atomic.LoadInt32(&dials); got != 1 {
		t.Fatalf("want exactly one dial, got %d", got)
	}
	waitState(t, m, "cl1", "appfw", "done")
	n := 0
	for _, r := range mc.Runs {
		if strings.Contains(r, "framework_create") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly one framework_create run, got %d: %v", n, mc.Runs)
	}
}

func TestManager_Cancel_ManualMarksCancelledAndRestartable(t *testing.T) {
	m, _ := newTestMgr(t, func(cmd string) ([]string, error) { return nil, nil })
	_, err := m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), true /*manual*/, false, nil)
	if err != nil {
		t.Fatal(err)
	}

	m.Cancel("cl1", "appfw")

	in, ok := m.Status("cl1", "appfw")
	if !ok || in.State != "cancelled" {
		t.Fatalf("state=%v ok=%v, want cancelled", in, ok)
	}

	if _, err := m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), true, false, nil); err != nil {
		t.Fatalf("restart after manual cancel rejected: %v", err)
	}
}

// blockingClient blocks the first Run on a caller-controlled channel and counts Close,
// so a test can hold a manual step provably in flight while racing Cancel against Next.
type blockingClient struct {
	started  chan struct{} // closed-signal: first Run has begun
	release  chan struct{} // test closes this to let the first Run return
	closes   int32
	gateOnce sync.Once
}

func (c *blockingClient) Run(ctx context.Context, cmd string, onLine func(string)) error {
	c.gateOnce.Do(func() {
		c.started <- struct{}{}
		<-c.release
	})
	return nil
}
func (c *blockingClient) Push(ctx context.Context, localPath, remotePath string) error { return nil }
func (c *blockingClient) Close() error {
	atomic.AddInt32(&c.closes, 1)
	return nil
}

// TestManager_Cancel_ManualRacesNext races Cancel against an in-flight manual Next step.
// It must not data-race, must land a single terminal state, must close the client exactly
// once, and the key must be restartable afterward.
func TestManager_Cancel_ManualRacesNext(t *testing.T) {
	dir := t.TempDir()
	appfw := filepath.Join(dir, "enterprise", "appfw")
	os.MkdirAll(appfw, 0o755)
	for _, f := range []string{"r.raw", "m.qcow2", "a.qcow2"} {
		os.WriteFile(filepath.Join(appfw, f), []byte("x"), 0o644)
	}
	st, _ := NewStore(filepath.Join(dir, "installs"))
	bc := &blockingClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	m := NewManager(st, NewDir(dir, filepath.Join(dir, "enterprise")), func(h, u, p string) (clusterssh.Client, error) { return bc, nil })

	if _, err := m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), true /*manual*/, false, nil); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); m.Next("cl1", "appfw") }()
	<-bc.started // Next is provably mid-execStep, blocked in the step's command
	go func() { defer wg.Done(); m.Cancel("cl1", "appfw") }()
	time.Sleep(50 * time.Millisecond) // let Cancel observe the in-flight step
	close(bc.release)                 // let the step's command return
	wg.Wait()

	in, ok := m.Status("cl1", "appfw")
	if !ok {
		t.Fatal("install vanished")
	}
	switch in.State {
	case "done", "error", "cancelled":
	default:
		t.Fatalf("non-terminal state after cancel/next race: %q", in.State)
	}
	if got := atomic.LoadInt32(&bc.closes); got != 1 {
		t.Fatalf("client Close called %d times, want exactly 1", got)
	}
	// Key must be restartable after the race settles.
	if _, err := m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), true, false, nil); err != nil {
		t.Fatalf("restart after cancel/next race rejected: %v", err)
	}
}

// --- helpers ---

func waitState(t *testing.T, m *Manager, cl, mod, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if in, ok := m.Status(cl, mod); ok && in.State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if in, ok := m.Status(cl, mod); ok {
		t.Fatalf("state=%q want %q", in.State, want)
	}
	t.Fatalf("no install; want state %q", want)
}

func containsCmd(runs []string, substr string) bool {
	for _, r := range runs {
		if strings.Contains(r, substr) {
			return true
		}
	}
	return false
}

func indexOfCmd(runs []string, substr string) int {
	for i, r := range runs {
		if strings.Contains(r, substr) {
			return i
		}
	}
	return -1
}

func stepState(in *Install, name string) StepState {
	for _, s := range in.Steps {
		if s.Name == name {
			return s.State
		}
	}
	return ""
}

func stepErr(in *Install, name string) string {
	for _, s := range in.Steps {
		if s.Name == name {
			return s.Err
		}
	}
	return ""
}

func validAppFWParams() InstallParams {
	return InstallParams{Project: "cmp", PublicNet: "public", MgmtNet: "public", LBIP: "10.32.36.120", OSImage: "r.raw", FsImage: "m.qcow2", LBImage: "a.qcow2"}
}

// A skipped step carries both timestamps but did none of the work, and the skip
// path is fast: framework_create skips in seconds when the framework is already
// active. Counting skips made the "typical ~" the UI shows for the longest step
// in the plan read as ~9s. Only StepDone counts.
func TestStepDurations_IgnoresSkippedErroredAndCancelledSteps(t *testing.T) {
	m, _ := newTestMgr(t, nil)
	step := func(name string, state StepState, start, end string) *Step {
		return &Step{Name: name, State: state, StartedAt: start, FinishedAt: end}
	}
	m.installs["c1/cmp"] = &Install{ClusterID: "c1", Module: "cmp", Steps: []*Step{
		// the real provisioning run: 40 minutes
		step("framework_create", StepDone, "2026-08-26T01:00:00Z", "2026-08-26T01:40:00Z"),
		step("preflight", StepDone, "2026-08-26T01:00:00Z", "2026-08-26T01:01:00Z"),
	}}
	m.installs["c2/cmp"] = &Install{ClusterID: "c2", Module: "cmp", Steps: []*Step{
		// three later runs that reused the framework: 9 seconds each
		step("framework_create", StepSkipped, "2026-08-26T02:00:00Z", "2026-08-26T02:00:09Z"),
		step("framework_create", StepError, "2026-08-26T03:00:00Z", "2026-08-26T03:00:03Z"),
		step("framework_create", StepSkipped, "2026-08-26T04:00:00Z", "2026-08-26T04:00:09Z"),
		// an in-flight step has no FinishedAt and must not count either
		{Name: "install_portal", State: StepActive, StartedAt: "2026-08-26T05:00:00Z"},
	}}

	got := m.StepDurations()
	if d := got["framework_create"]; d != 2400 {
		t.Fatalf("framework_create typical = %v, want 2400 (the one real run, not the skips)", d)
	}
	if d := got["preflight"]; d != 60 {
		t.Fatalf("preflight typical = %v, want 60", d)
	}
	if _, ok := got["install_portal"]; ok {
		t.Fatalf("install_portal should be absent while still running, got %v", got["install_portal"])
	}
}

// The LB-IP probes must consider the wire, not just this cluster's neutron: on
// a provider network shared with another cluster, neutron has no record of the
// other cluster's ports (issue #100).
func TestLBIPProbesDetectOnWireHolders(t *testing.T) {
	if !strings.Contains(lbIPPoolProbe, "arping") {
		t.Error("lbIPPoolProbe does not probe the wire; a free-by-neutron address may still be in use")
	}
	// ARP specifically: an Octavia amphora answers neither ICMP nor, unless it
	// is listening, TCP.
	for _, bad := range []string{"ping -c", "nc -z"} {
		if strings.Contains(lbIPPoolProbe, bad) {
			t.Errorf("lbIPPoolProbe uses %q, which misses an amphora", bad)
		}
	}
	if !strings.Contains(lbIPTakenProbe, "arping") {
		t.Error("lbIPTakenProbe (preflight backstop) does not probe the wire")
	}
}

// lbIPTakenProbe binds the address once into a shell variable, so it takes
// exactly one Sprintf argument; passing two silently appends %!(EXTRA ...).
func TestLBIPTakenProbeTakesOneArg(t *testing.T) {
	if got := strings.Count(lbIPTakenProbe, "%s"); got != 1 {
		t.Fatalf("lbIPTakenProbe has %d %%s verbs, want 1", got)
	}
	out := fmt.Sprintf(lbIPTakenProbe, "10.32.1.101")
	if strings.Contains(out, "%!") {
		t.Errorf("Sprintf produced a formatting error: %s", out)
	}
	if !strings.Contains(out, "addr=10.32.1.101") {
		t.Errorf("address not bound into the probe: %s", out)
	}
}

// The embedded python must actually compile — a syntax error there degrades
// the suggestion to empty with no visible cause.
func TestLBIPProbeEmbeddedPythonCompiles(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	start := strings.Index(lbIPPoolProbe, "python3 -c '")
	if start < 0 {
		t.Fatal("no embedded python found in lbIPPoolProbe")
	}
	body := lbIPPoolProbe[start+len("python3 -c '"):]
	end := strings.Index(body, "'")
	if end < 0 {
		t.Fatal("unterminated embedded python in lbIPPoolProbe")
	}
	cmd := exec.Command(py, "-c", "import sys;compile(sys.stdin.read(),\"probe\",\"exec\")")
	cmd.Stdin = strings.NewReader(body[:end])
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("embedded python does not compile: %v\n%s", err, out)
	}
}

// A step whose SSH transport died must not read as a command that failed.
//
// On the lab cluster both advisor install steps reported failure with an empty
// output while the identical command, run on the node, succeeded -- so an
// operator was told the install had failed while it was in fact still running.
// The step still stops (nothing can read a result once the channel is gone),
// but what it says has to send the operator to the cluster rather than back to
// the driver's retry button.
func TestManager_TransportLoss_SaysTheCommandMayStillBeRunning(t *testing.T) {
	m, _ := newTestMgr(t, frameworkActiveAfterCreate("appfw", func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "app_import") {
			return nil, fmt.Errorf("%s: %w", cmd, clusterssh.ErrConnectionLost)
		}
		return nil, nil
	}))
	m.Start("cl1", "advisor", "10.32.10.140", "pw",
		InstallParams{Project: "appfw", Framework: "appfw", LBIP: "10.32.36.120", OSImage: "r.raw",
			AdvisorFile: "cube-advisor-1.2.3.pigz", AdvisorLBIP: "10.0.0.9"}, false, false, nil)
	waitState(t, m, "cl1", "advisor", "error")

	in, _ := m.Status("cl1", "advisor")
	var step *Step
	for i := range in.Steps {
		if in.Steps[i].Name == "advisor_register" {
			step = in.Steps[i]
		}
	}
	if step == nil {
		t.Fatalf("advisor_register step missing: %+v", in.Steps)
	}
	if step.State != StepError {
		t.Fatalf("advisor_register state = %q, want %q", step.State, StepError)
	}
	if !strings.Contains(step.Err, "still be running on the cluster") {
		t.Fatalf("advisor_register Err = %q, want it to point at the cluster", step.Err)
	}
}

// The other half: a command that really did fail must keep reading as a command
// failure. Misclassifying it would send an operator to the cluster to look for
// work that genuinely failed in the driver.
func TestManager_CommandFailure_StillReadsAsOne(t *testing.T) {
	m, _ := newTestMgr(t, frameworkActiveAfterCreate("appfw", func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "app_import") {
			return nil, fmt.Errorf("%s: exited 1 (import.sh: chart push failed)", cmd)
		}
		return nil, nil
	}))
	m.Start("cl1", "advisor", "10.32.10.140", "pw",
		InstallParams{Project: "appfw", Framework: "appfw", LBIP: "10.32.36.120", OSImage: "r.raw",
			AdvisorFile: "cube-advisor-1.2.3.pigz", AdvisorLBIP: "10.0.0.9"}, false, false, nil)
	waitState(t, m, "cl1", "advisor", "error")

	in, _ := m.Status("cl1", "advisor")
	for _, s := range in.Steps {
		if s.Name == "advisor_register" && strings.Contains(s.Err, "still be running") {
			t.Fatalf("a real command failure was reported as a lost connection: %q", s.Err)
		}
	}
}

// The pool probe takes a count, so it must be Sprintf'd -- using it raw emits
// %!d(MISSING) into the remote command and the suggestion silently degrades
// to empty.
func TestLBIPPoolProbeTakesACount(t *testing.T) {
	if got := strings.Count(lbIPPoolProbe, "%d"); got != 1 {
		t.Fatalf("lbIPPoolProbe has %d %%d verbs, want 1", got)
	}
	out := fmt.Sprintf(lbIPPoolProbe, 5)
	if strings.Contains(out, "%!") {
		t.Errorf("Sprintf produced a formatting error: %s", out)
	}
	if !strings.Contains(out, `"5"`) {
		t.Errorf("count not bound into the probe: %s", out)
	}
}

// Every console origin address is checked before the install commits to it.
// They become SANs on a certificate issued during the run, so an address that
// turns out to be taken is not a value to edit afterwards.
func TestPreflightChecksEveryConsoleAddress(t *testing.T) {
	var probed []string
	var mu sync.Mutex
	m, _ := newTestMgr(t, frameworkActiveAfterCreate("appfw", func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "addr=") {
			mu.Lock()
			probed = append(probed, cmd)
			mu.Unlock()
			return []string{"free"}, nil
		}
		return nil, nil
	}))
	m.Start("cl1", "advisor", "10.32.10.140", "pw",
		InstallParams{Project: "appfw", Framework: "appfw", LBIP: "10.32.36.120", OSImage: "r.raw",
			AdvisorFile: "cube-advisor-1.2.3.pigz", AdvisorLBIP: "10.0.0.9",
			AdvisorPool: []string{"10.0.0.10", "10.0.0.11"}}, false, false, nil)
	waitState(t, m, "cl1", "advisor", "done")

	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{"addr=10.0.0.10", "addr=10.0.0.11"} {
		found := false
		for _, c := range probed {
			if strings.Contains(c, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("console address %q was never probed; probed=%v", want, probed)
		}
	}
}

// A taken console address stops the install, rather than being discovered when
// the console refuses to start.
func TestPreflightRefusesATakenConsoleAddress(t *testing.T) {
	m, _ := newTestMgr(t, frameworkActiveAfterCreate("appfw", func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "addr=10.0.0.11") {
			return []string{"taken"}, nil
		}
		if strings.Contains(cmd, "addr=") {
			return []string{"free"}, nil
		}
		return nil, nil
	}))
	m.Start("cl1", "advisor", "10.32.10.140", "pw",
		InstallParams{Project: "appfw", Framework: "appfw", LBIP: "10.32.36.120", OSImage: "r.raw",
			AdvisorFile: "cube-advisor-1.2.3.pigz", AdvisorLBIP: "10.0.0.9",
			AdvisorPool: []string{"10.0.0.10", "10.0.0.11"}}, false, false, nil)
	waitState(t, m, "cl1", "advisor", "error")

	in, _ := m.Status("cl1", "advisor")
	if !strings.Contains(in.Steps[0].Err, "10.0.0.11") {
		t.Fatalf("preflight Err = %q, want it to name the taken address", in.Steps[0].Err)
	}
}

// An advisor being upgraded already holds its own console addresses: each
// origin is a LoadBalancer Service on exactly the pool it was given. Probing
// them reports the deployment being upgraded as a collision, which refused
// every re-install of an advisor that had a console — found on the 1cc r630,
// where preflight blocked the upgrade and nothing downstream ran.
func TestPreflightSkipsConsoleAddressesTheAdvisorAlreadyOwns(t *testing.T) {
	var probed []string
	var mu sync.Mutex
	m, _ := newTestMgr(t, frameworkActiveAfterCreate("appfw", func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "cube-advisor-web-") {
			// This advisor already serves .60; .61 is new to it.
			return []string{"10.0.0.60"}, nil
		}
		if strings.Contains(cmd, "addr=") {
			mu.Lock()
			probed = append(probed, cmd)
			mu.Unlock()
			return []string{"free"}, nil
		}
		return nil, nil
	}))
	m.Start("cl1", "advisor", "10.32.10.140", "pw",
		InstallParams{Project: "appfw", Framework: "appfw", LBIP: "10.32.36.120", OSImage: "r.raw",
			AdvisorFile: "cube-advisor-1.2.3.pigz", AdvisorLBIP: "10.0.0.9",
			AdvisorPool: []string{"10.0.0.60"}}, false, false, nil)
	waitState(t, m, "cl1", "advisor", "done")

	mu.Lock()
	defer mu.Unlock()
	for _, c := range probed {
		if strings.Contains(c, "addr=10.0.0.60") {
			t.Fatalf("preflight probed an address this advisor already owns: %q", c)
		}
	}
}

// The other half: an address that is genuinely new to this advisor is still
// probed, or the fix would trade a blocked upgrade for a silent collision.
func TestPreflightStillProbesAddressesNewToTheAdvisor(t *testing.T) {
	m, _ := newTestMgr(t, frameworkActiveAfterCreate("appfw", func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "cube-advisor-web-") {
			return []string{"10.0.0.60"}, nil // owns .60 only
		}
		if strings.Contains(cmd, "addr=10.0.0.61") {
			return []string{"taken"}, nil
		}
		if strings.Contains(cmd, "addr=") {
			return []string{"free"}, nil
		}
		return nil, nil
	}))
	m.Start("cl1", "advisor", "10.32.10.140", "pw",
		InstallParams{Project: "appfw", Framework: "appfw", LBIP: "10.32.36.120", OSImage: "r.raw",
			AdvisorFile: "cube-advisor-1.2.3.pigz", AdvisorLBIP: "10.0.0.9",
			AdvisorPool: []string{"10.0.0.60", "10.0.0.61"}}, false, false, nil)
	waitState(t, m, "cl1", "advisor", "error")

	in, _ := m.Status("cl1", "advisor")
	if !strings.Contains(in.Steps[0].Err, "10.0.0.61") {
		t.Fatalf("preflight Err = %q, want it to name the address that is new and taken", in.Steps[0].Err)
	}
}

// Images the extpack already imported into glance need no staged local file.
func TestManager_AppFW_ImagesFromExtpack_NoLocalFiles(t *testing.T) {
	m, mc := newTestMgr(t, frameworkActiveAfterCreate("cmp", nil))
	for _, f := range []string{"r.raw", "m.qcow2", "a.qcow2"} {
		os.Remove(filepath.Join(m.dir.Get(), "appfw", f))
	}
	m.Start("cl1", "appfw", "10.32.10.140", "pw",
		InstallParams{Project: "cmp", PublicNet: "public", MgmtNet: "public", LBIP: "10.32.36.120", OSImage: "r.raw", FsImage: "m.qcow2", LBImage: "a.qcow2"}, false, false, nil)
	waitState(t, m, "cl1", "appfw", "done")
	if containsCmd(mc.Runs, "import local r.raw") || !containsCmd(mc.Runs, "framework_create cmp public public 10.32.36.120 r") {
		t.Fatalf("runs=%v", mc.Runs)
	}
}

// A missing local file whose image is not in glance either still fails preflight,
// and the error points at the extpack.
func TestManager_AppFW_MissingImageNotInGlance_FailsPreflight(t *testing.T) {
	m, _ := newTestMgr(t, func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "image show r") {
			return nil, errors.New("No Image found")
		}
		return nil, nil
	})
	os.Remove(filepath.Join(m.dir.Get(), "appfw", "r.raw"))
	m.Start("cl1", "appfw", "10.32.10.140", "pw",
		InstallParams{Project: "cmp", PublicNet: "public", MgmtNet: "public", LBIP: "10.32.36.120", OSImage: "r.raw", FsImage: "m.qcow2", LBImage: "a.qcow2"}, false, false, nil)
	waitState(t, m, "cl1", "appfw", "error")
	in, _ := m.Status("cl1", "appfw")
	var msg string
	for _, s := range in.Steps {
		if s.Name == "preflight" {
			msg = s.Err
		}
	}
	if !strings.Contains(msg, "missing artifact: r.raw") || !strings.Contains(msg, "import_extpack") {
		t.Fatalf("preflight err=%q", msg)
	}
}

func TestScpRun_PresentSkipsUploadAndImport(t *testing.T) {
	mc := &clusterssh.MockClient{} // every command succeeds: presence check passes
	ps := plannedStep{Name: "import_extpack", Kind: "scp+run", Cmd: "IMPORT", Present: "PRESENT",
		LocalPath: filepath.Join(t.TempDir(), "x.ext"), RemotePath: cephfsGlance}
	skipped, err := scpRun(context.Background(), mc, ps, func(string) {})
	if err != nil || !skipped {
		t.Fatalf("skipped=%v err=%v", skipped, err)
	}
	if len(mc.Pushes) != 0 || containsRun(mc.Runs, "IMPORT") {
		t.Fatalf("pushed %v / ran %v despite presence", mc.Pushes, mc.Runs)
	}
}

func TestScpRun_PresentVerifiedAfterImport(t *testing.T) {
	imported := false
	mc := &clusterssh.MockClient{Script: func(cmd string) ([]string, error) {
		switch {
		case cmd == "IMPORT":
			imported = true
		case cmd == "PRESENT" && !imported:
			return nil, errors.New("absent")
		}
		return nil, nil
	}}
	local := filepath.Join(t.TempDir(), "x.ext")
	os.WriteFile(local, []byte("ext"), 0o644)
	ps := plannedStep{Kind: "scp+run", Cmd: "IMPORT", Present: "PRESENT", LocalPath: local, RemotePath: cephfsGlance}
	if skipped, err := scpRun(context.Background(), mc, ps, func(string) {}); err != nil || skipped {
		t.Fatalf("skipped=%v err=%v", skipped, err)
	}
	if len(mc.Pushes) != 1 || !imported {
		t.Fatalf("pushes=%v imported=%v", mc.Pushes, imported)
	}
}

func TestScpRun_PresentStillAbsentAfterImportFails(t *testing.T) {
	mc := &clusterssh.MockClient{Script: func(cmd string) ([]string, error) {
		if cmd == "PRESENT" {
			return nil, errors.New("absent")
		}
		return nil, nil
	}}
	local := filepath.Join(t.TempDir(), "x.ext")
	os.WriteFile(local, []byte("ext"), 0o644)
	ps := plannedStep{Kind: "scp+run", Cmd: "IMPORT", Present: "PRESENT", LocalPath: local, RemotePath: cephfsGlance}
	if _, err := scpRun(context.Background(), mc, ps, func(string) {}); err == nil || !strings.Contains(err.Error(), "x.ext") {
		t.Fatalf("an import that leaves the images absent must fail naming the extpack, got %v", err)
	}
}

func containsRun(runs []string, cmd string) bool {
	for _, r := range runs {
		if r == cmd {
			return true
		}
	}
	return false
}

func TestManager_WarningsPrintedFirstInPreflight(t *testing.T) {
	m, _ := newTestMgr(t, frameworkActiveAfterCreate("cmp", nil))
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false, []string{"not in the matrix"})
	waitState(t, m, "cl1", "appfw", "done")
	in, _ := m.Status("cl1", "appfw")
	if !strings.HasPrefix(in.Steps[0].Output, "⚠ not in the matrix\n") {
		t.Fatalf("preflight output = %q", in.Steps[0].Output)
	}
}

// writeAdvisorManifest drops a manifest named "t1" (optionally with a trusted key) under the data dir.
func writeAdvisorManifest(t *testing.T, m *Manager, key *TrustKey) *Manifest {
	mf := &Manifest{Name: "t1"}
	mf.Match.Version = "9.9.9"
	mf.Modules = map[string][]ModuleEntry{ModuleAdvisor: {{Version: "1.2.3", Status: "supported"}}}
	if key != nil {
		mf.Trust = &Trust{AdvisorReleaseKey: key}
	}
	raw, _ := json.Marshal(mf)
	md := filepath.Join(m.dir.Get(), "manifests")
	os.MkdirAll(md, 0o755)
	os.WriteFile(filepath.Join(md, "t1.json"), raw, 0o644)
	return mf
}

// etcVersion wraps a script so `cat /etc/version` reports the test cluster version.
func etcVersion(v string, next func(string) ([]string, error)) func(string) ([]string, error) {
	return func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "cat /etc/version") {
			return []string{v}, nil
		}
		return next(cmd)
	}
}

const testClusterVersion = "CUBE_9.9.9_20260101-0000_abc123"

func advisorParams() InstallParams {
	return InstallParams{Project: "appfw", Framework: "appfw", LBIP: "10.32.36.120", OSImage: "r.raw",
		AdvisorFile: "cube-advisor-1.2.3.pigz", AdvisorLBIP: "10.0.0.9"}
}

func TestPreflight_AdvisorSignatureChecked(t *testing.T) {
	m, _ := newTestMgr(t, etcVersion(testClusterVersion, frameworkActiveAfterCreate("appfw", nil)))
	k := newTestKeys(t)
	mf := writeAdvisorManifest(t, m, k.pub)
	m.Start("cl1", "advisor", "10.32.10.140", "pw", advisorParams(), false, false, nil, mf)
	waitState(t, m, "cl1", "advisor", "error")
	in, _ := m.Status("cl1", "advisor")
	if !strings.Contains(in.Steps[0].Output, "Verifying the advisor bundle") {
		t.Fatalf("preflight output = %q", in.Steps[0].Output)
	}
}

func TestPreflight_AdvisorNoKeyWarns(t *testing.T) {
	m, _ := newTestMgr(t, etcVersion(testClusterVersion, frameworkActiveAfterCreate("appfw", nil)))
	mf := writeAdvisorManifest(t, m, nil)
	m.Start("cl1", "advisor", "10.32.10.140", "pw", advisorParams(), false, false, nil, mf)
	waitState(t, m, "cl1", "advisor", "done")
	in, _ := m.Status("cl1", "advisor")
	if !strings.Contains(in.Steps[0].Output, `⚠ advisor bundle signature not checked: no trusted release key for manifest "t1"`) {
		t.Fatalf("preflight output = %q", in.Steps[0].Output)
	}
}

func TestPreflight_AdvisorUninstallSkipsSignatureCheck(t *testing.T) {
	m, _ := newTestMgr(t, frameworkActiveAfterCreate("appfw", nil))
	k := newTestKeys(t)
	writeAdvisorManifest(t, m, k.pub)
	in := &Install{Module: ModuleAdvisor, Op: "uninstall", Manifest: "t1", Params: advisorParams()}
	var lines []string
	if err := m.preflight(context.Background(), &clusterssh.MockClient{Script: frameworkActiveAfterCreate("appfw", nil)}, in, nil, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	if out := strings.Join(lines, "\n"); strings.Contains(out, "advisor bundle") {
		t.Fatalf("uninstall must not check the bundle: %q", out)
	}
}

func writeCmpManifest(t *testing.T, m *Manager, name, version string) {
	mf := &Manifest{Name: name, Modules: map[string][]ModuleEntry{ModuleCMP: {{Version: "2.1.0", Status: "supported"}}}}
	mf.Match.Version = version
	raw, _ := json.Marshal(mf)
	md := filepath.Join(m.dir.Get(), "manifests")
	os.MkdirAll(md, 0o755)
	os.WriteFile(filepath.Join(md, name+".json"), raw, 0o644)
}

func runMatrixPreflight(m *Manager, in *Install) (string, error) {
	var lines []string
	mc := &clusterssh.MockClient{Script: etcVersion(testClusterVersion, frameworkActiveAfterCreate("appfw", nil))}
	err := m.preflight(context.Background(), mc, in, nil, func(l string) { lines = append(lines, l) })
	return strings.Join(lines, "\n"), err
}

func cmpInstall(file, manifest string, lab bool) *Install {
	p := advisorParams()
	p.AppFile = file
	return &Install{Module: ModuleCMP, Manifest: manifest, Lab: lab, Params: p}
}

func TestPreflight_MatchedManifestBlocksUnlistedCmpWithoutClientManifest(t *testing.T) {
	m, _ := newTestMgr(t, nil)
	writeCmpManifest(t, m, "vT", "9.9.9")
	if _, err := runMatrixPreflight(m, cmpInstall("cube-portal-3.0.0.pigz", "", false)); err == nil || !strings.Contains(err.Error(), "support matrix") {
		t.Fatalf("err = %v, want matrix refusal", err)
	}
	if out, err := runMatrixPreflight(m, cmpInstall("cube-portal-2.1.0.pigz", "", false)); err != nil && strings.Contains(err.Error(), "support matrix") {
		t.Fatalf("listed version refused: %v (%s)", err, out)
	}
}

func TestPreflight_ClientManifestDifferingFromMatchIsRefusedUnlessLab(t *testing.T) {
	m, _ := newTestMgr(t, nil)
	writeCmpManifest(t, m, "vT", "9.9.9")
	writeCmpManifest(t, m, "vOther", "1.0.0")
	if _, err := runMatrixPreflight(m, cmpInstall("cube-portal-2.1.0.pigz", "vOther", false)); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("err = %v, want differing-manifest refusal", err)
	}
	if out, err := runMatrixPreflight(m, cmpInstall("cube-portal-2.1.0.pigz", "vOther", true)); (err != nil && strings.Contains(err.Error(), "differs")) || !strings.Contains(out, "lab install, continuing") {
		t.Fatalf("lab: err = %v, out = %q", err, out)
	}
}

func TestPreflight_AdvisorWithoutClientManifestIsSignatureChecked(t *testing.T) {
	m, _ := newTestMgr(t, nil)
	k := newTestKeys(t)
	writeAdvisorManifest(t, m, k.pub)
	in := &Install{Module: ModuleAdvisor, Params: advisorParams()}
	out, err := runMatrixPreflight(m, in)
	if err == nil || !strings.Contains(out, "Verifying the advisor bundle is signed with t1") {
		t.Fatalf("err = %v, out = %q; want signature check against matched t1 key", err, out)
	}
}

func TestPreflight_NoMatchingManifestNoConstraints(t *testing.T) {
	m, _ := newTestMgr(t, nil)
	writeCmpManifest(t, m, "vOther", "1.0.0")
	if out, err := runMatrixPreflight(m, cmpInstall("cube-portal-3.0.0.pigz", "", false)); err != nil && strings.Contains(err.Error(), "support matrix") {
		t.Fatalf("no matching manifest must not constrain: %v (%s)", err, out)
	}
}

func TestPreflight_UnreadableClusterVersionFails(t *testing.T) {
	m, _ := newTestMgr(t, nil)
	writeCmpManifest(t, m, "vT", "9.9.9")
	base := frameworkActiveAfterCreate("appfw", nil)
	cases := map[string]func(string) ([]string, error){
		"error": func(cmd string) ([]string, error) {
			if strings.Contains(cmd, "cat /etc/version") {
				return nil, fmt.Errorf("ssh boom")
			}
			return base(cmd)
		},
		"empty": func(cmd string) ([]string, error) {
			if strings.Contains(cmd, "cat /etc/version") {
				return []string{"  ", ""}, nil
			}
			return base(cmd)
		},
	}
	for name, script := range cases {
		mc := &clusterssh.MockClient{Script: script}
		err := m.preflight(context.Background(), mc, cmpInstall("cube-portal-2.1.0.pigz", "", false), nil, func(string) {})
		if err == nil || !strings.Contains(err.Error(), "cannot read the cluster version (/etc/version)") {
			t.Fatalf("%s: err = %v, want version-unreadable refusal", name, err)
		}
	}
}
