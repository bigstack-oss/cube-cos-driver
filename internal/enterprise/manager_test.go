package enterprise

import (
	"context"
	"errors"
	"os"
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
	mc := &clusterssh.MockClient{Script: script}
	return NewManager(st, NewDir(dir, filepath.Join(dir, "enterprise")), func(h, u, p string) (clusterssh.Client, error) { return mc, nil }), mc
}

func TestManager_AppFW_AutoRunsAllStepsInOrder(t *testing.T) {
	m, mc := newTestMgr(t, frameworkActiveAfterCreate("cmp", nil))
	in, err := m.Start("cl1", "appfw", "10.32.10.140", "pw",
		InstallParams{Project: "cmp", PublicNet: "public", MgmtNet: "public", LBIP: "10.32.36.120", OSImage: "r.raw", FsImage: "m.qcow2", LBImage: "a.qcow2"}, false, false)
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
	m.Start("cl1", "cmp", "10.32.10.140", "pw", InstallParams{Project: "cmp", LBIP: "10.32.36.120", OSImage: "r.raw", AppFile: "cube-portal-2.1.0.pigz"}, false, false)
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
	m.Start("cl1", "cmp", "10.32.10.140", "pw", InstallParams{Project: "cmp", Framework: "cmp", AppFile: "cube-portal-2.1.0.pigz", LBIP: "10.32.36.120"}, false, false)
	waitState(t, m, "cl1", "cmp", "done")
	if containsCmd(mc.Runs, "framework_create cmp") {
		t.Fatalf("should not recreate an active framework: %v", mc.Runs)
	}
	in, _ := m.Status("cl1", "cmp")
	if stepState(in, "framework_create") != "skipped" {
		t.Fatalf("framework_create should be skipped, got %s", stepState(in, "framework_create"))
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
		AdvisorFile: "cube-advisor-1.2.3.pigz", AdvisorLBIP: "10.0.0.9", LBIP: "10.32.36.120"}, false, false)
	waitState(t, m, "cl1", "advisor", "done")
	if containsCmd(mc.Runs, "framework_create appfw") {
		t.Fatalf("should not recreate an active framework: %v", mc.Runs)
	}
	in, _ := m.Status("cl1", "advisor")
	if stepState(in, "framework_create") != "skipped" {
		t.Fatalf("framework_create should be skipped, got %s", stepState(in, "framework_create"))
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
	m.Start("cl1", "advisor", "10.32.10.140", "pw", InstallParams{Project: "appfw", Framework: "appfw",
		AdvisorFile: "cube-advisor-1.2.3.pigz", AdvisorLBIP: "10.0.0.9", LBIP: "10.32.36.120"}, false, false)
	waitState(t, m, "cl1", "advisor", "done")

	m.StartUninstall("cl1", "appfw", "10.32.10.140", "pw", InstallParams{Project: "appfw"}, false)
	waitState(t, m, "cl1", "appfw", "done")

	if _, ok := m.Status("cl1", "advisor"); ok {
		t.Fatal("advisor install record should be dropped after appfw uninstall")
	}
}

func TestManager_Manual_NextAdvancesOneStep(t *testing.T) {
	m, _ := newTestMgr(t, func(cmd string) ([]string, error) { return nil, nil })
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), true /*manual*/, false)
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
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false)
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
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, true /*airgap*/)
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
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false)
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
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false)
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
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false)
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
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false)
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
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false)
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
	m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false)
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
			_, err := m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), false, false)
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
	_, err := m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), true /*manual*/, false)
	if err != nil {
		t.Fatal(err)
	}

	m.Cancel("cl1", "appfw")

	in, ok := m.Status("cl1", "appfw")
	if !ok || in.State != "cancelled" {
		t.Fatalf("state=%v ok=%v, want cancelled", in, ok)
	}

	if _, err := m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), true, false); err != nil {
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

	if _, err := m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), true /*manual*/, false); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); m.Next("cl1", "appfw") }()
	<-bc.started // Next is provably mid-execStep, blocked in the step's command
	go func() { defer wg.Done(); m.Cancel("cl1", "appfw") }()
	time.Sleep(50 * time.Millisecond) // let Cancel observe the in-flight step
	close(bc.release)                  // let the step's command return
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
	if _, err := m.Start("cl1", "appfw", "10.32.10.140", "pw", validAppFWParams(), true, false); err != nil {
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
