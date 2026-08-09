package enterprise

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bigstack-oss/cube-cos-driver/internal/clusterssh"
)

// Manager orchestrates enterprise install runs, one goroutine per (cluster,module).
type Manager struct {
	store   *Store
	dataDir string
	dial    func(host, user, pw string) (clusterssh.Client, error)

	mu       sync.Mutex
	installs map[string]*Install
	cancels  map[string]context.CancelFunc
	clients  map[string]clusterssh.Client
	plans    map[string][]plannedStep
	// busy[k] marks a manual Next step mid-execStep; cancelReq[k] records a Cancel
	// that arrived during that step so the in-flight Next finalizes as cancelled.
	busy      map[string]bool
	cancelReq map[string]bool
}

// NewManager returns a Manager persisting to store and reading artifacts from dataDir.
func NewManager(store *Store, dataDir string, dial func(host, user, pw string) (clusterssh.Client, error)) *Manager {
	m := &Manager{
		store:    store,
		dataDir:  dataDir,
		dial:     dial,
		installs:  map[string]*Install{},
		cancels:   map[string]context.CancelFunc{},
		clients:   map[string]clusterssh.Client{},
		plans:     map[string][]plannedStep{},
		busy:      map[string]bool{},
		cancelReq: map[string]bool{},
	}
	m.rehydrate()
	materializePortalScript(dataDir)
	return m
}

// rehydrate reloads persisted installs into memory on startup. A run persisted
// as "running" has no live goroutine after a restart, so it's marked failed
// (its active step too) — the operator sees the interruption instead of a stale
// "running" or a 404, and can re-run.
func (m *Manager) rehydrate() {
	for _, in := range m.store.List() {
		if in.State == "running" {
			in.State = "error"
			for _, s := range in.Steps {
				if s.State == StepActive {
					s.State = StepError
					if s.Err == "" {
						s.Err = "driver restarted while this step was running"
					}
				}
			}
			_ = m.store.Save(in)
		}
		m.installs[key(in.ClusterID, in.Module)] = in
	}
}

// List returns snapshot copies of every known install, newest first, for the
// dashboard.
func (m *Manager) List() []*Install {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Install, 0, len(m.installs))
	for _, in := range m.installs {
		out = append(out, copyInstall(in))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out
}

func key(clusterID, module string) string { return clusterID + "/" + module }

// Start reserves the key, builds the plan, persists the Install, and (unless manual) runs it.
func (m *Manager) Start(clusterID, module, vip, password string, p InstallParams, manual, airgap bool, mf ...*Manifest) (*Install, error) {
	var manifest *Manifest
	if len(mf) > 0 {
		manifest = mf[0]
	}
	k := key(clusterID, module)

	in := &Install{
		ClusterID:      clusterID,
		Module:         module,
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		Manual:         manual,
		SimulateAirgap: airgap,
		Params:         p,
		Current:        0,
		State:          "running",
	}

	// Reserve the key atomically (check+insert in one critical section) so a concurrent
	// Start is rejected before we release the lock to dial — closes the TOCTOU window.
	m.mu.Lock()
	if ex, ok := m.installs[k]; ok && ex.State == "running" {
		m.mu.Unlock()
		return nil, fmt.Errorf("install already running for %s/%s", clusterID, module)
	}
	m.installs[k] = in
	m.mu.Unlock()

	client, err := m.dial(vip, "root", password)
	if err != nil {
		m.rollback(k, in, client) // free the reservation so the key is restartable
		return nil, err
	}

	plan := BuildPlan(module, p, airgap, m.dataDir, manifest)

	steps := make([]*Step, 0, len(plan))
	for _, ps := range plan {
		steps = append(steps, &Step{Name: ps.Name, Title: ps.Title, State: StepPending})
	}

	m.mu.Lock()
	in.Steps = steps
	m.clients[k] = client
	m.plans[k] = plan
	if err := m.store.Save(in); err != nil {
		m.mu.Unlock()
		m.rollback(k, in, client)
		return nil, err
	}
	if !manual {
		ctx, cancel := context.WithCancel(context.Background())
		m.cancels[k] = cancel
		go m.runAll(ctx, cancel, k, in, client, plan)
	}
	m.mu.Unlock()

	return in, nil
}

// Status returns a snapshot copy of the install so callers never race the runner.
func (m *Manager) Status(clusterID, module string) (*Install, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.installs[key(clusterID, module)]
	if !ok {
		return nil, false
	}
	return copyInstall(in), true
}

// Next runs the current step of a manual install and advances, cleaning up when terminal.
// It is the single writer of the terminal transition + cleanup for a manual install: a
// concurrent Cancel that arrives mid-step only records cancelReq, and Next honors it here.
func (m *Manager) Next(clusterID, module string) error {
	k := key(clusterID, module)
	m.mu.Lock()
	in := m.installs[k]
	if in == nil {
		m.mu.Unlock()
		return fmt.Errorf("no install for %s/%s", clusterID, module)
	}
	if in.State != "running" { // already terminal (e.g. a non-busy Cancel won the lock)
		m.mu.Unlock()
		return nil
	}
	if m.busy[k] {
		m.mu.Unlock()
		return fmt.Errorf("step already in progress for %s/%s", clusterID, module)
	}
	client := m.clients[k]
	plan := m.plans[k]
	idx := in.Current
	if idx >= len(plan) {
		m.mu.Unlock()
		return nil
	}
	m.busy[k] = true // claim the step; a racing Cancel will only set cancelReq
	m.mu.Unlock()

	err := m.execStep(context.Background(), k, client, in, plan, idx)

	m.mu.Lock()
	delete(m.busy, k)
	cancelled := m.cancelReq[k]
	delete(m.cancelReq, k)
	// Advance/persist only while running and not cancelled; a terminal step already saved.
	if !cancelled && err == nil && in.State == "running" {
		in.Current++
		if in.Current >= len(plan) {
			in.State = "done"
		}
		m.store.Save(in)
	}
	terminal := cancelled || in.State != "running"
	m.mu.Unlock()

	if cancelled {
		m.terminate(k, in, "cancelled") // honor a Cancel requested during this step
	}
	if terminal {
		m.cleanup(k, in, nil, client)
	}
	return err
}

// Cancel stops an in-flight auto run (runAll lands a terminal state and cleans up via its
// own defer). For a manual install there is no cancel func/goroutine: if a Next step is
// in flight, only flag cancelReq and let that Next finalize (never terminate/cleanup here,
// so cleanup runs at most once); otherwise mark cancelled under the lock — which trips the
// running-guard in any later Next — then terminate + clean up. Terminal/unknown = no-op.
func (m *Manager) Cancel(clusterID, module string) {
	k := key(clusterID, module)
	m.mu.Lock()
	if c, ok := m.cancels[k]; ok {
		m.mu.Unlock()
		c()
		return
	}
	in := m.installs[k]
	if in == nil || in.State != "running" {
		m.mu.Unlock()
		return
	}
	if m.busy[k] {
		m.cancelReq[k] = true // in-flight Next owns the terminal transition + cleanup
		m.mu.Unlock()
		return
	}
	in.State = "cancelled" // trip Next's running-guard before releasing the lock
	client := m.clients[k]
	m.mu.Unlock()

	m.terminate(k, in, "cancelled")
	m.cleanup(k, in, nil, client)
}

// runAll executes steps from Current to the end, stopping on error or cancel.
func (m *Manager) runAll(ctx context.Context, cancel context.CancelFunc, k string, in *Install, client clusterssh.Client, plan []plannedStep) {
	defer m.cleanup(k, in, cancel, client) // release resources + prune maps on any exit

	for {
		if ctx.Err() != nil {
			m.terminate(k, in, "cancelled") // single terminal save
			return
		}
		m.mu.Lock()
		idx := in.Current
		m.mu.Unlock()
		if idx >= len(plan) {
			break
		}
		if err := m.execStep(ctx, k, client, in, plan, idx); err != nil {
			return // execStep already persisted the error state
		}
		m.mu.Lock()
		// A terminal step (complete) already persisted "done"; stop without more saves.
		if in.State != "running" {
			m.mu.Unlock()
			return
		}
		in.Current++
		m.store.Save(in)
		m.mu.Unlock()
	}

	// All steps ran (appfw has no complete step): finalize once.
	m.mu.Lock()
	in.State = "done"
	m.store.Save(in)
	m.mu.Unlock()
}

// execStep runs one planned step, updating and persisting the install.
func (m *Manager) execStep(ctx context.Context, k string, client clusterssh.Client, in *Install, plan []plannedStep, idx int) error {
	ps := plan[idx]
	step := in.Steps[idx]

	m.mu.Lock()
	step.State = StepActive
	m.store.Save(in)
	m.mu.Unlock()

	// onLine flushes each line to the step's output live (under the lock) so a
	// polling client sees progress while the step is still running.
	onLine := func(l string) {
		m.mu.Lock()
		step.Output += l + "\n"
		m.mu.Unlock()
	}

	var runErr error
	skipped := false
	switch ps.Kind {
	case "detect":
		runErr = m.preflight(ctx, client, in, plan, onLine)
	case "airgap":
		onLine("Applying air-gap simulation…")
		runErr = client.Run(ctx, ps.Cmd, onLine)
	case "run":
		onLine(step.Title + "…")
		runErr = client.Run(ctx, ps.Cmd, onLine)
	case "scp+run":
		skipped, runErr = scpRun(ctx, client, ps, onLine)
	case "framework":
		skipped, runErr = runFramework(ctx, client, ps, onLine)
	case "complete":
		// finalized below
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Some CubeCOS commands print an error yet still exit 0 (hex_cli
	// "Invalid arguments."; appctl framework init failures). Treat those output
	// markers as failures for the command steps so a step can't false-succeed.
	if runErr == nil && !skipped && (ps.Kind == "run" || ps.Kind == "scp+run") {
		if marker := cliFailureMarker(step.Output); marker != "" {
			runErr = fmt.Errorf("%s (see output)", marker)
		}
	}
	if runErr != nil {
		step.State = StepError
		step.Err = runErr.Error()
		in.State = "error"
		m.store.Save(in)
		return runErr
	}
	if skipped {
		step.State = StepSkipped
	} else {
		step.State = StepDone
		// Close out the narration so a finished step never reads as still-running
		// (the last streamed line is otherwise a present-continuous "Installing…").
		switch ps.Kind {
		case "airgap", "run", "scp+run", "framework":
			step.Output += "✓ " + step.Title + " — done.\n"
		}
	}
	if ps.Kind == "complete" {
		in.Portal = "https://" + in.Params.LBIP + "/portal"
		in.State = "done"
	}
	m.store.Save(in)
	return nil
}

// cliFailureMarker returns a human message when the step output contains a
// known CubeCOS failure signature that the command emits while still exiting 0.
func cliFailureMarker(output string) string {
	switch {
	case strings.Contains(output, "Invalid arguments."):
		return "cluster rejected the command (invalid arguments)"
	case strings.Contains(output, "openpgp: key expired"):
		return "app-framework tooling failed: signing key expired on the cluster"
	case strings.Contains(output, "failed to init"):
		return "app-framework failed to initialize on the cluster"
	}
	return ""
}

// preflight verifies reachability, framework freshness, and artifact presence.
func (m *Manager) preflight(ctx context.Context, client clusterssh.Client, in *Install, plan []plannedStep, onLine func(string)) error {
	onLine("Checking cluster reachability…")
	if err := client.Run(ctx, "cubectl node exec -p 'hostname'", onLine); err != nil {
		return err
	}

	onLine("Listing existing app frameworks…")
	var fw []string
	collect := func(l string) { fw = append(fw, l); onLine(l) }
	if err := client.Run(ctx, "hex_cli -c app -c framework_list", collect); err != nil {
		return err
	}

	// framework_create is idempotent (skip if active, wait if still provisioning,
	// create if absent), so a present framework is not an error — just note it.
	if planHasStep(plan, "framework_create") {
		name := in.Params.Project
		if name != "" && listHasName(fw, name) {
			onLine(fmt.Sprintf("App-framework %q already present — the create step will verify it's active (or wait), not recreate it.", name))
		}
	}

	// Every file the plan will push (images, appctl, the CMP .pigz) must exist
	// and be non-empty — a 0-byte placeholder must fail here, not mid-install.
	onLine("Verifying staged artifacts…")
	for _, ps := range plan {
		if ps.Kind != "scp+run" || ps.LocalPath == "" {
			continue
		}
		name := filepath.Base(ps.LocalPath)
		fi, err := os.Stat(ps.LocalPath)
		if err != nil {
			return fmt.Errorf("missing artifact: %s", name)
		}
		if fi.Size() == 0 {
			return fmt.Errorf("artifact is empty (0 bytes): %s", name)
		}
		// Integrity: if an <artifact>.md5 sidecar is staged, the file must match it.
		// Catches a truncated/corrupt copy here — before it's imported and a broken
		// image silently hangs cluster provisioning for an hour.
		if md5Path := ps.LocalPath + ".md5"; fileExists(md5Path) {
			want, rerr := readMD5Sidecar(md5Path)
			if rerr != nil {
				return fmt.Errorf("cannot read %s.md5: %v", name, rerr)
			}
			onLine(fmt.Sprintf("  checking integrity of %s (%s)…", name, humanSize(fi.Size())))
			got, herr := fileMD5(ps.LocalPath)
			if herr != nil {
				return fmt.Errorf("cannot hash %s: %v", name, herr)
			}
			if !strings.EqualFold(got, want) {
				return fmt.Errorf("artifact %s failed integrity check (md5 %s, expected %s) — the staged file is truncated or corrupt; re-copy it from the source", name, got, want)
			}
			onLine(fmt.Sprintf("  ✓ %s (%s, md5 ok)", name, humanSize(fi.Size())))
			continue
		}
		onLine(fmt.Sprintf("  ✓ %s (%s)", name, humanSize(fi.Size())))
	}
	onLine("Preflight checks passed.")
	return nil
}

// readMD5Sidecar reads the first whitespace-delimited token from an .md5 file
// (handles both "<hash>" and "<hash>  <filename>" forms), lowercased.
func readMD5Sidecar(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return "", fmt.Errorf("empty md5 file")
	}
	return strings.ToLower(f[0]), nil
}

// fileMD5 streams the file to compute its md5 (constant memory).
func fileMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// humanSize formats a byte count as a compact human-readable string.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// scpRun imports an image idempotently: skip if it already exists, else push+run.
func scpRun(ctx context.Context, client clusterssh.Client, ps plannedStep, onLine func(string)) (skipped bool, err error) {
	discard := func(string) {}
	file := filepath.Base(ps.LocalPath)
	if ps.ImageName != "" {
		onLine(fmt.Sprintf("Checking if image %q already exists…", ps.ImageName))
		show := "source /etc/admin-openrc.sh && openstack image show " + ps.ImageName
		if showErr := client.Run(ctx, show, discard); showErr == nil {
			onLine(fmt.Sprintf("Image %q already exists — skipping import.", ps.ImageName))
			return true, nil
		}
		onLine(fmt.Sprintf("Image %q not found — importing.", ps.ImageName))
	}
	// Skip the (potentially large) upload if the same file is already staged on
	// the cluster from a prior run — a size match guards against partial uploads.
	remote := ps.RemotePath + "/" + file
	if fi, statErr := os.Stat(ps.LocalPath); statErr == nil &&
		remoteFileSize(ctx, client, remote) == fi.Size() {
		onLine(fmt.Sprintf("%s already staged on the cluster — skipping upload.", file))
	} else {
		onLine(fmt.Sprintf("Uploading %s to %s…", file, ps.RemotePath))
		if err := client.Push(ctx, ps.LocalPath, ps.RemotePath); err != nil {
			return false, err
		}
	}
	// "Importing" only fits the glance image steps; a plain binary/package push
	// (appctl, the CMP .pigz) is an install, not an import.
	verb := "Importing"
	if ps.ImageName == "" {
		verb = "Installing"
	}
	onLine(fmt.Sprintf("%s %s…", verb, file))
	if err := client.Run(ctx, ps.Cmd, onLine); err != nil {
		return false, err
	}
	// Don't trust the CLI exit code — confirm the image actually landed in
	// glance (the import CLI can print an error yet exit 0).
	if ps.ImageName != "" {
		onLine(fmt.Sprintf("Verifying image %q was created…", ps.ImageName))
		verify := "source /etc/admin-openrc.sh && openstack image show " + ps.ImageName
		if vErr := client.Run(ctx, verify, discard); vErr != nil {
			return false, fmt.Errorf("import finished but image %q was not created (check the command output)", ps.ImageName)
		}
		onLine(fmt.Sprintf("Image %q confirmed.", ps.ImageName))
	}
	return false, nil
}

// remoteFileSize returns the byte size of a remote file, or -1 when absent.
func remoteFileSize(ctx context.Context, client clusterssh.Client, path string) int64 {
	var out []string
	cmd := fmt.Sprintf("stat -c %%s %q 2>/dev/null || true", path)
	client.Run(ctx, cmd, func(l string) {
		if s := strings.TrimSpace(l); s != "" {
			out = append(out, s)
		}
	})
	if len(out) == 0 {
		return -1
	}
	n, err := strconv.ParseInt(out[0], 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// Heavy app-framework provisioning (Keystone project + networks + an RKE2 VM
// cluster) can take a long time; poll its status until active. Vars (not consts)
// so tests can shorten them.
var (
	frameworkReadyTimeout = time.Hour
	frameworkPollInterval = 30 * time.Second
)

// frameworkStates classifies the STATUS column of `framework_list` (which is the
// Rancher cluster STATE): "ready" is usable, "error" is terminal-bad, "" (any
// other known token) is still working.
var frameworkStates = map[string]string{
	"active":       "ready",
	"updating":     "working",
	"provisioning": "working",
	"creating":     "working",
	"reconciling":  "working",
	"pending":      "working",
	"waiting":      "working",
	"error":        "error",
	"failed":       "error",
	"unavailable":  "error",
	"removing":     "error",
}

// frameworkStatus runs framework_list and returns the STATUS token for name and
// whether name is present. Column layout varies (PROVIDER may be blank), so it
// scans the name's row for the first known status token.
func frameworkStatus(ctx context.Context, client clusterssh.Client, name string) (status string, found bool) {
	var lines []string
	client.Run(ctx, "hex_cli -c app -c framework_list", func(l string) { lines = append(lines, l) })
	for _, l := range lines {
		fields := strings.Fields(l)
		named := false
		for _, f := range fields {
			if f == name {
				named = true
				break
			}
		}
		if !named {
			continue
		}
		for _, f := range fields {
			if _, ok := frameworkStates[strings.ToLower(f)]; ok {
				return strings.ToLower(f), true
			}
		}
		return "", true // row present but no recognised status token
	}
	return "", false
}

const frameworkKubectl = "KUBECONFIG=/etc/rancher/k3s/k3s.yaml kubectl"

// frameworkProgress returns a short human summary of the framework's RKE2 node
// provisioning (CAPI machine phases + a representative blocking message), so the
// poll narration reports *what* it's waiting on instead of a bare "waiting". It's
// best-effort: any query failure yields "" and the caller just omits it.
func frameworkProgress(ctx context.Context, client clusterssh.Client, name string) string {
	var phases []string
	client.Run(ctx, fmt.Sprintf("%s get machines.cluster.x-k8s.io -A -l cluster.x-k8s.io/cluster-name=%s --no-headers -o custom-columns=P:.status.phase 2>/dev/null", frameworkKubectl, name),
		func(l string) {
			if s := strings.TrimSpace(l); s != "" && s != "<none>" {
				phases = append(phases, s)
			}
		})
	if len(phases) == 0 {
		return ""
	}
	counts := map[string]int{}
	running := 0
	for _, p := range phases {
		counts[p]++
		if p == "Running" {
			running++
		}
	}
	parts := []string{fmt.Sprintf("nodes %d/%d ready", running, len(phases))}
	var pb []string
	for p, c := range counts {
		if p != "Running" {
			pb = append(pb, fmt.Sprintf("%d %s", c, p))
		}
	}
	sort.Strings(pb)
	if len(pb) > 0 {
		parts = append(parts, strings.Join(pb, ", "))
	}
	// A representative blocking message from the first not-ready machine's
	// conditions (the useful one — "waiting for agent to check in" — is often
	// status Unknown, so take all non-empty messages, not just status==False).
	jp := `-o jsonpath='{range .items[0].status.conditions[*]}{.message}{"\n"}{end}'`
	msgCmd := fmt.Sprintf("%s get machines.cluster.x-k8s.io -A -l cluster.x-k8s.io/cluster-name=%s %s 2>/dev/null", frameworkKubectl, name, jp)
	var msg []string
	client.Run(ctx, msgCmd, func(l string) {
		if s := strings.TrimSpace(l); s != "" {
			msg = append(msg, s)
		}
	})
	out := strings.Join(parts, "; ")
	if len(msg) > 0 {
		out += " — " + msg[len(msg)-1] // most recent/most specific
	}
	return out
}

// ensureAmphoraTag guarantees the octavia amphora image carries the "amphora"
// tag that Octavia requires (amp_image_tag=amphora) — without it the ingress
// load balancer can't spawn an amphora and stays in ERROR.
func ensureAmphoraTag(ctx context.Context, client clusterssh.Client, onLine func(string)) error {
	var out []string
	client.Run(ctx, "source /etc/admin-openrc.sh && openstack image show amphora-x64-haproxy -f value -c tags 2>/dev/null",
		func(l string) { out = append(out, l) })
	if strings.Contains(strings.Join(out, " "), "amphora") {
		return nil
	}
	onLine("Amphora image is missing the 'amphora' tag Octavia requires — tagging it…")
	return client.Run(ctx, "source /etc/admin-openrc.sh && openstack image set --tag amphora amphora-x64-haproxy", onLine)
}

// verifyFrameworkRegistry confirms the framework's in-cluster Harbor registry is
// reachable through its ingress LB. If it isn't, appctl's registry setup (the
// registry-details secret) didn't complete — which silently breaks app_register
// image refs. Failing here surfaces the real cause instead of a broken deploy.
func verifyFrameworkRegistry(ctx context.Context, client clusterssh.Client, name, lbip string, onLine func(string)) error {
	if lbip == "" {
		return nil // can't verify without the ingress LB IP
	}
	host := name + ".registry.cubecos.com"
	onLine("Verifying the app-framework registry (Harbor) is reachable…")
	cmd := fmt.Sprintf("curl -sk -o /dev/null -w '%%{http_code}' --resolve %s:443:%s --max-time 15 https://%s/api/v2.0/systeminfo 2>/dev/null",
		host, lbip, host)
	var out []string
	client.Run(ctx, cmd, func(l string) { out = append(out, strings.TrimSpace(l)) })
	code := strings.Join(out, "")
	if code == "200" {
		onLine("App-framework registry reachable.")
		return nil
	}
	return fmt.Errorf("app-framework registry (Harbor at %s → %s:443) not reachable (HTTP %q); appctl's registry setup did not complete, so app_register will produce broken image refs — check the ingress Octavia LB (openstack loadbalancer list) and the amphora image tag", host, lbip, code)
}

// runFramework creates the app-framework if absent, then polls until its Rancher
// cluster reaches "active" — mirroring CubeCOS's own app_framework_deploy wait
// loop. It's idempotent: an already-active framework is skipped; an existing but
// not-yet-active one is waited on rather than recreated. skipped=true means the
// framework was already active (no work done).
func runFramework(ctx context.Context, client clusterssh.Client, ps plannedStep, onLine func(string)) (skipped bool, err error) {
	name := ps.Framework
	// Octavia needs the amphora image tagged (amp_image_tag=amphora) to spawn the
	// ingress load balancer; an untagged image leaves the LB stuck in ERROR, which
	// silently breaks the framework's registry setup. Ensure the tag up front.
	if err := ensureAmphoraTag(ctx, client, onLine); err != nil {
		return false, err
	}
	st, found := frameworkStatus(ctx, client, name)
	switch {
	case found && st == "active":
		onLine(fmt.Sprintf("App-framework %q is already active — skipping creation.", name))
		return true, verifyFrameworkRegistry(ctx, client, name, ps.LBIP, onLine)
	case found:
		onLine(fmt.Sprintf("App-framework %q already exists (state: %s) — not recreating; waiting for it to become active.", name, statusLabel(st)))
	default:
		onLine(fmt.Sprintf("Creating app-framework %q (this provisions an RKE2 cluster and can take many minutes)…", name))
		var out []string
		tee := func(l string) { out = append(out, l); onLine(l) }
		if runErr := client.Run(ctx, ps.Cmd, tee); runErr != nil {
			return false, runErr
		}
		if marker := cliFailureMarker(strings.Join(out, "\n")); marker != "" {
			return false, fmt.Errorf("%s (see output)", marker)
		}
	}

	// Poll until active / error / timeout.
	start := time.Now()
	for {
		st, found := frameworkStatus(ctx, client, name)
		switch {
		case found && st == "active":
			onLine(fmt.Sprintf("App-framework %q is active.", name))
			return false, verifyFrameworkRegistry(ctx, client, name, ps.LBIP, onLine)
		case found && frameworkStates[st] == "error":
			return false, fmt.Errorf("app-framework %q entered state %q — check `hex_cli -c app -c framework_list` and the Rancher cluster", name, st)
		}
		elapsed := time.Since(start)
		progress := frameworkProgress(ctx, client, name)
		if elapsed >= frameworkReadyTimeout {
			detail := ""
			if progress != "" {
				detail = " [" + progress + "]"
			}
			return false, fmt.Errorf("WARNING: app-framework %q not active after %s (last state: %s)%s; provisioning may still be in progress — verify with `hex_cli -c app -c framework_list` and inspect the RKE2 nodes (kubectl get machines -A), then re-run to resume or delete the framework to start over",
				name, frameworkReadyTimeout, statusLabel(st), detail)
		}
		line := fmt.Sprintf("Waiting for app-framework %q to become active — state: %s, %s elapsed", name, statusLabel(st), elapsed.Round(time.Second))
		if progress != "" {
			line += "; " + progress
		}
		onLine(line + "…")
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(frameworkPollInterval):
		}
	}
}

// statusLabel renders a framework status token for narration ("unknown" when
// the framework has dropped out of the list mid-provision).
func statusLabel(st string) string {
	if st == "" {
		return "unknown"
	}
	return st
}

// rollback frees a reservation (before the runner owns the key) so the key is restartable.
func (m *Manager) rollback(k string, in *Install, client clusterssh.Client) {
	m.mu.Lock()
	if m.installs[k] == in {
		delete(m.installs, k)
		delete(m.clients, k)
		delete(m.plans, k)
	}
	m.mu.Unlock()
	if client != nil {
		client.Close()
	}
}

// terminate lands a persisted terminal state (e.g. cancelled) and flags any active step.
func (m *Manager) terminate(k string, in *Install, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in.State = state
	for _, s := range in.Steps {
		if s.State == StepActive {
			s.State = StepError
			s.Err = state
		}
	}
	m.store.Save(in)
}

// cleanup closes THIS run's client and, only if a restart hasn't replaced it, releases
// its cancel func and prunes runtime maps — mirrors rollback's pointer-identity guard.
func (m *Manager) cleanup(k string, in *Install, cancel context.CancelFunc, client clusterssh.Client) {
	if cancel != nil {
		cancel() // release our own context regardless of map ownership
	}
	m.mu.Lock()
	if m.installs[k] == in {
		delete(m.cancels, k)
		delete(m.clients, k)
		delete(m.plans, k)
		delete(m.busy, k)
		delete(m.cancelReq, k)
	}
	m.mu.Unlock()
	if client != nil {
		client.Close()
	}
}

func copyInstall(in *Install) *Install {
	c := *in
	c.Steps = make([]*Step, len(in.Steps))
	for i, s := range in.Steps {
		sc := *s
		c.Steps[i] = &sc
	}
	return &c
}

func planHasStep(plan []plannedStep, name string) bool {
	for _, ps := range plan {
		if ps.Name == name {
			return true
		}
	}
	return false
}

func listHasName(lines []string, name string) bool {
	for _, l := range lines {
		for _, f := range strings.Fields(l) {
			if f == name {
				return true
			}
		}
	}
	return false
}

func sliceHas(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// ClusterQuery is the live cluster info used to populate the install form.
type ClusterQuery struct {
	Projects         []string
	Networks         []string
	AirgapSupported  bool
	SuggestedLBIP    string
	SuggestedStorage string   // default cinder volume type, for image import
	Version          string   // CubeCOS version from /etc/version, e.g. "3.1.0"
	Manifest         string   // auto-matched manifest name ("" if none)
	Manifests        []string // available manifest names, for the version picker
}

// lbIPProbe extracts the start of the "public" network's allocation pool — a
// sensible default LB IP (an address in the cluster's external range).
const lbIPProbe = `source /etc/admin-openrc.sh && sub=$(openstack subnet list --network public -f value -c ID | head -1) && [ -n "$sub" ] && openstack subnet show "$sub" -f json | python3 -c 'import sys,json; p=json.load(sys.stdin).get("allocation_pools") or []; print(p[0]["start"] if p else "")'`

// storageProbe reads the cluster's default cinder volume type (falling back to
// the first type) — the storage_backend the image-import CLI validates against.
const storageProbe = `source /etc/admin-openrc.sh && { openstack volume type list --default -f value -c Name 2>/dev/null; openstack volume type list -f value -c Name 2>/dev/null; } | grep -v '^$' | head -1`

// Introspect dials the cluster and gathers projects, networks, air-gap support,
// and a suggested LB IP for the install form. The client is closed on return.
func (m *Manager) Introspect(host, password string) (ClusterQuery, error) {
	var q ClusterQuery
	c, err := m.dial(host, "root", password)
	if err != nil {
		return q, err
	}
	defer c.Close()
	// Projects the image-import CLI accepts as <tenant> for the default domain
	// (openstack's full project list can include ones import rejects).
	q.Projects, err = sshList(c, "hex_sdk os_list_project_by_domain_basic default")
	if err != nil {
		return ClusterQuery{}, err
	}
	q.Networks, err = sshList(c, "source /etc/admin-openrc.sh && openstack network list -f value -c Name")
	if err != nil {
		return ClusterQuery{}, err
	}
	// Air-gap simulation relies on a hex_sdk function present only on newer
	// images (absent on e.g. 3.1.0).
	out, _ := sshList(c, "grep -rlqw airgap_sim_apply /usr/lib/hex_sdk/modules/ && echo yes || echo no")
	q.AirgapSupported = len(out) > 0 && out[0] == "yes"
	// Best-effort suggested LB IP; empty when the probe can't determine one.
	if lb, _ := sshList(c, lbIPProbe); len(lb) > 0 {
		q.SuggestedLBIP = lb[0]
	}
	// Default storage backend read from the cluster (not hardcoded).
	if st, _ := sshList(c, storageProbe); len(st) > 0 {
		q.SuggestedStorage = st[0]
	}
	// Detect the CubeCOS version and auto-match a manifest.
	manifests := LoadManifests(m.dataDir)
	q.Manifests = ManifestNames(manifests)
	if ver, _ := sshList(c, "cat /etc/version"); len(ver) > 0 {
		version, build, commit := ParseVersion(ver[0])
		q.Version = version
		if mf := MatchManifest(manifests, version, build, commit); mf != nil {
			q.Manifest = mf.Name
			q.AirgapSupported = mf.AirgapSupported
		}
	}
	return q, nil
}

// sshList runs cmd and returns its non-empty output lines.
func sshList(c clusterssh.Client, cmd string) ([]string, error) {
	var out []string
	err := c.Run(context.Background(), cmd, func(l string) {
		if s := strings.TrimSpace(l); s != "" {
			out = append(out, s)
		}
	})
	return out, err
}
