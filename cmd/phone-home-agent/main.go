// phone-home-agent runs on first boot of an unconfigured CubeCOS node. It
// phones home to the snapshot server, syncs its clock, runs a network
// preflight, and pulls + applies its appointed snapshot. Ships inside the
// CubeCOS image; launched by a systemd unit gated on the node being
// unconfigured.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bigstack-oss/cube-cos-driver/internal/agent"
)

const configuredMarker = "/etc/appliance/state/configured"

// logFile tees the agent log to a file on the node so it survives (the console
// isn't retrievable on boards with no working SoL). /run is tmpfs — persists
// for the installer session; the OS-phase daemon also writes /var/log.
func setupLogging(preflight bool) {
	path := "/var/log/phone-home-agent.log"
	if preflight {
		path = "/run/phone-home-agent.log"
	}
	writers := []io.Writer{os.Stderr}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		writers = append(writers, f)
	}
	// Mirror progress to the operator-visible consoles: /dev/tty0 is the VGA
	// screen the BMC KVM shows; /dev/console goes to the last console= (serial
	// here). Userspace writes to /dev/console never reach the VGA screen, so
	// tty0 is required for KVM visibility. Write to both, best effort.
	for _, dev := range []string{"/dev/tty0", "/dev/console"} {
		if c, err := os.OpenFile(dev, os.O_WRONLY, 0); err == nil {
			writers = append(writers, c)
		}
	}
	log.SetOutput(io.MultiWriter(writers...))
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("=== phone-home-agent start (preflight=%v) ===", preflight)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// serverFromCmdline reads driver_server=<url> from /proc/cmdline, if present.
func serverFromCmdline() string {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return ""
	}
	for _, tok := range strings.Fields(string(data)) {
		if v, ok := strings.CutPrefix(tok, "driver_server="); ok {
			return v
		}
	}
	return ""
}

func macs() []string {
	var out []string
	entries, _ := os.ReadDir("/sys/class/net")
	for _, e := range entries {
		if e.Name() == "lo" {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/sys/class/net", e.Name(), "address"))
		if err != nil {
			continue
		}
		mac := strings.TrimSpace(string(b))
		if mac != "" && mac != "00:00:00:00:00:00" {
			out = append(out, mac)
		}
	}
	return out
}

func serial() string {
	b, _ := os.ReadFile("/sys/class/dmi/id/product_serial")
	return strings.TrimSpace(string(b))
}

func setClock(t time.Time) error {
	tv := unix.NsecToTimeval(t.UnixNano())
	return unix.Settimeofday(&tv)
}

func probe(target string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "ping", "-c", "1", "-W", "2", target).Run() == nil
}

// stagedSnapshot is the RAM path the installer pre-fetches the snapshot to
// (before topology reconfig); hex_autoinstall copies it to /store post-restore.
const stagedSnapshot = "/run/appointed.snapshot"

// stampEnv writes the node's OS-phase identity to RAM; hex_autoinstall appends
// it to the restored OS's phone-home-agent.env so the daemon knows its role. It
// also drops the picked OS disk to /run/os-disk, which hex_autoinstall reads to
// target the restore (overriding its auto-detect) so the OS lands on the
// operator-chosen local disk, never a SAN LUN.
func stampEnv(host, cluster string, isMaster bool, osDisk string) {
	m := "0"
	if isMaster {
		m = "1"
	}
	content := fmt.Sprintf("CUBE_HOSTNAME=%s\nCUBE_CLUSTER_ID=%s\nIS_MASTER=%s\nCUBE_OS_DISK=%s\n", host, cluster, m, osDisk)
	_ = os.WriteFile("/run/phone-home-agent.env", []byte(content), 0o644)
	if osDisk != "" {
		_ = os.WriteFile("/run/os-disk", []byte(osDisk+"\n"), 0o644)
	}
}

// fetchSnapshot downloads the appointed snapshot to RAM during preflight.
func fetchSnapshot(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.Create(stagedSnapshot)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// localSnapshot is where the installer stashed this node's snapshot post-restore
// (persisted from the preflight pre-fetch, into the OS rootfs). Applying it
// needs no network — the whole point of pre-fetching during install.
const localSnapshot = "/var/support/appointed.snapshot"

// hex_config exit status is a bitmask (hex/include/hex/config_module.h):
//   1 = EXIT_FAILURE (a component failed — snapshot_apply ROLLS BACK its changes)
//   2 = CONFIG_EXIT_NEED_REBOOT ("caller should reboot the system")
// Success is 0; a clean apply that only needs a reboot to activate is 2. Exit 3
// = failure|reboot means a component FAILED and was rolled back (the reboot bit
// just reflects reboot-state touched) — rebooting does NOT fix it, so exit 3 is
// a real failure. Reboot only for the clean case (reboot bit set, failure clear).
const (
	cfgExitFailure    = 1
	cfgExitNeedReboot = 1 << 1
)

// errApplyReboot signals that snapshot_apply needs a reboot to make progress —
// the OS-phase two-phase boot. The caller reboots (bounded) and re-applies.
var errApplyReboot = errors.New("snapshot_apply: reboot required")

const (
	// applyRebootFile persists the reboot count across the two-phase reboots so
	// the agent bounds them (a module that never converges must not reboot-loop).
	applyRebootFile = "/var/support/apply-reboots"
	// applyTerminalFile marks a terminal apply outcome (real failure). Present →
	// the agent will NOT re-apply on a later boot; it re-reports and stops.
	applyTerminalFile = "/var/support/apply-terminal"
	maxApplyReboots   = 3
)

func readApplyReboots() int {
	b, _ := os.ReadFile(applyRebootFile)
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

func writeApplyReboots(n int) {
	_ = os.WriteFile(applyRebootFile, []byte(strconv.Itoa(n)), 0o644)
}

func apply(ctx context.Context, snapshotURL string) error {
	dest := localSnapshot
	if _, err := os.Stat(dest); err == nil {
		log.Printf("applying pre-staged snapshot %s (local, no download)", dest)
	} else {
		// Fallback: nothing pre-staged — download it (needs the OS network up).
		dest = "/var/support/appointed.snapshot"
		if derr := download(ctx, snapshotURL, dest); derr != nil {
			return derr
		}
	}
	out, err := exec.CommandContext(ctx, "hex_config", "snapshot_apply", dest).CombinedOutput()
	if err == nil {
		return nil
	}
	code := -1
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	// Clean reboot-needed (exit 2: reboot bit set, NO failure bit) → the apply
	// succeeded and only needs a reboot to activate. This is the legitimate
	// two-phase case: reboot, then resume. A set failure bit means it rolled
	// back — never reboot-retry that.
	if code >= 0 && code&cfgExitNeedReboot != 0 && code&cfgExitFailure == 0 {
		log.Printf("snapshot_apply exit %d: needs reboot to activate (no failure) — rebooting", code)
		return errApplyReboot
	}
	// Soft-complete: some phases exit non-zero yet FTS set the configured marker.
	if _, e := os.Stat(configuredMarker); e == nil {
		log.Printf("snapshot_apply exit %d but %s exists — treating as success", code, configuredMarker)
		return nil
	}
	// Real failure (component failed + rolled back, e.g. exit 3) — no retry.
	return fmt.Errorf("snapshot_apply failed (exit %d): %s", code, strings.TrimSpace(string(out)))
}

func download(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	_ = os.MkdirAll(filepath.Dir(dest), 0o755)
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// reportRestoreDone tells the server the image restore finished (installer,
// just before reboot) so the deploy UI can show restore-complete / reboot.
func reportRestoreDone(srv string) {
	body, _ := json.Marshal(map[string]any{"macs": macs(), "serial": serial()})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", srv+"/api/v1/agents/restore-done", bytes.NewReader(body))
	if err != nil {
		log.Printf("restore-done: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("restore-done post: %v", err)
		return
	}
	resp.Body.Close()
	log.Printf("restore-done reported (HTTP %d)", resp.StatusCode)
}

// reportApplyStarted tells the server the OS-phase agent is up (reboot done) and
// about to apply — so the deploy UI flips reboot→done, apply→active immediately,
// rather than sitting on "rebooting" for the whole apply.
func reportApplyStarted(srv string) {
	body, _ := json.Marshal(map[string]any{"macs": macs(), "serial": serial()})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", srv+"/api/v1/agents/apply-started", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
}

// reportApplyFailed tells the server the snapshot apply failed terminally, so
// the deploy UI shows the node errored instead of hanging on "applying".
func reportApplyFailed(srv, cluster, host, msg string) {
	body, _ := json.Marshal(map[string]any{
		"clusterId": cluster, "hostname": host, "message": msg,
		"macs": macs(), "serial": serial(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", srv+"/api/v1/agents/apply-failed", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
}

func main() {
	server := flag.String("server", envOr("DRIVER_SERVER", ""), "snapshot server URL (default: driver_server= from kernel cmdline)")
	poll := flag.Duration("poll", 15*time.Second, "check-in poll interval")
	preflight := flag.Bool("preflight", false, "installer-phase network preflight (pre-restore); blocks until green light 1")
	restoreDone := flag.Bool("restore-done", false, "report restore-complete to the server (installer, just before reboot) and exit")
	inventory := flag.Bool("inventory", false, "hardware discovery (inspect boot): report CPU/mem/disk/NIC to the server, then halt")
	inspectCheck := flag.Bool("inspect-check", false, "quick pre-fetch check: if the server marks this node for inspect, inventory + halt (skip the image fetch); else exit 0")
	flag.Parse()
	setupLogging(*preflight)

	// Already configured → nothing to do.
	if _, err := os.Stat(configuredMarker); err == nil {
		log.Printf("node already configured; exiting")
		return
	}

	srv := *server
	if srv == "" {
		srv = serverFromCmdline()
	}
	if srv == "" {
		srv = "http://192.168.1.150" // default pxeserver IP (PXESERVER_IP)
	}
	if !strings.HasPrefix(srv, "http") {
		srv = "http://" + srv
	}

	if *inventory {
		reportInventory(srv)
		return
	}

	if *inspectCheck {
		runInspectCheck(srv)
		return
	}

	if *restoreDone {
		reportRestoreDone(srv)
		return
	}

	if *preflight {
		runPreflight(srv, *poll)
		return
	}

	// Preferred OS-phase path: the installer pre-fetched this node's snapshot
	// and stamped its role, so we apply the local file and coordinate
	// master-first entirely over SEL — no in-band network until after apply.
	if _, err := os.Stat(localSnapshot); err == nil {
		runLocalApply(srv, *poll)
		return
	}

	deps := agent.Deps{
		Identity:   func() ([]string, string) { return macs(), serial() },
		SetClock:   setClock,
		Probe:      probe,
		Apply:      apply,
		Configured: func() bool { _, err := os.Stat(configuredMarker); return err == nil },
		HTTP:       &http.Client{Timeout: 30 * time.Second},
		Now:        time.Now,
		Sleep:      time.Sleep,
	}
	log.Printf("phone-home-agent: server=%s (no local snapshot — in-band flow)", srv)
	if err := agent.Run(context.Background(), srv, deps, *poll); err != nil {
		log.Fatalf("agent: %v", err)
	}
	log.Printf("phone-home-agent: snapshot applied")
}

// runLocalApply applies the pre-staged snapshot with master-first ordering
// coordinated purely over SEL: the master applies at once and its "applied"
// report makes the server drop a "go" SEL record on each non-master's BMC; a
// non-master waits for that record (local KCS read, no network) before applying.
func runLocalApply(srv string, poll time.Duration) {
	ctx := context.Background()
	isMaster := os.Getenv("IS_MASTER") == "1"
	host := os.Getenv("CUBE_HOSTNAME")
	cluster := os.Getenv("CUBE_CLUSTER_ID")
	log.Printf("local-apply: host=%s cluster=%s master=%v", host, cluster, isMaster)

	// A prior boot already reached a terminal failure — never re-apply a genuine
	// failure. Re-report it (in case the server missed it) and stop.
	if _, e := os.Stat(applyTerminalFile); e == nil {
		log.Printf("local-apply: terminal apply state already reached — not re-applying")
		reportApplyFailed(srv, cluster, host, "apply previously failed (terminal state)")
		return
	}

	reportApplyStarted(srv) // agent up (reboot done): flip UI to applying

	if !isMaster {
		log.Printf("local-apply: non-master — waiting for master 'go' via SEL (OOB) ...")
		if !waitSELGate(ctx, poll) {
			log.Printf("local-apply: gate wait ended without go signal")
			return
		}
		log.Printf("local-apply: SEL 'go' seen — master finished, applying local snapshot")
	} else {
		log.Printf("local-apply: master — applying local snapshot immediately")
	}

	err := apply(ctx, "") // empty URL: apply() uses localSnapshot
	if errors.Is(err, errApplyReboot) {
		// CubeCOS two-phase: reboot so the pending module (e.g. keystone) commits,
		// then the OS-phase agent resumes on the next boot. Bounded.
		if n := readApplyReboots(); n < maxApplyReboots {
			writeApplyReboots(n + 1)
			_ = writeSEL("apply", "reboot", fmt.Sprintf("%d/%d", n+1, maxApplyReboots))
			log.Printf("local-apply: reboot required — rebooting to continue (attempt %d/%d)", n+1, maxApplyReboots)
			_ = exec.Command("systemctl", "reboot").Run()
			_ = exec.Command("reboot").Run()
			select {} // block until the reboot takes the node down
		}
		err = fmt.Errorf("did not converge after %d reboots", maxApplyReboots)
	}
	if err != nil {
		// Real failure — do NOT retry. Mark terminal, report to the driver so the
		// node shows errored (not endlessly rebooting), then exit cleanly.
		_ = os.WriteFile(applyTerminalFile, []byte("failed\n"), 0o644)
		_ = writeSEL("apply", "error", err.Error())
		reportApplyFailed(srv, cluster, host, err.Error())
		log.Printf("local-apply: FAILED (no retry): %v", err)
		return
	}
	_ = writeSEL("applied", "ok", host) // mark our own SEL: apply done
	// The snapshot just configured our network; report in-band so the server
	// releases the non-masters (if we are master) and records completion.
	reportApplied(srv, cluster, host, isMaster)
	log.Printf("local-apply: applied + reported done")

	// FTS finalization: the master runs `cluster set_ready` once, after every
	// node has applied (it needs cluster-wide OSDs/services up). This is what
	// turns the configured nodes into a ready, usable cluster.
	if isMaster {
		runSetReady(ctx, srv, cluster, host)
	}
}

// runSetReady polls the server for the operator's UI-supplied set_ready input
// (external CIDR / gateway / floating-IP pool), then runs the one-time cluster
// set_ready on the master with those args and reports the cluster ready. The
// args require operator input, so this is UI-driven — the agent only executes.
func runSetReady(ctx context.Context, srv, cluster, host string) {
	log.Printf("set_ready: master configured — waiting for operator set_ready input from the UI ...")
	var sr setReadyInput
	for {
		if s, ok := pollSetReady(srv, cluster); ok && s.Trigger {
			sr = s
			break
		}
		time.Sleep(15 * time.Second)
	}
	args := []string{"-c", "cluster", "-c", "set_ready"}
	for _, a := range []string{sr.CIDR, sr.Gateway, sr.IPRange} {
		if a != "" {
			args = append(args, a)
		}
	}
	log.Printf("set_ready: running hex_cli %v", args)
	cmd := exec.CommandContext(ctx, "hex_cli", args...)
	// set_ready prompts "Create a shared external network? Enter 'YES'": the UI
	// already captured the operator's intent, so auto-confirm here.
	if sr.CreateExternal {
		cmd.Stdin = strings.NewReader("YES\n")
	} else {
		cmd.Stdin = strings.NewReader("\n")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("set_ready: %v: %s", err, strings.TrimSpace(string(out)))
		postJSON(srv+"/api/v1/agents/ready", map[string]any{"clusterId": cluster, "hostname": host, "ok": false, "message": strings.TrimSpace(string(out))})
		return
	}
	log.Printf("set_ready: cluster ready")
	postJSON(srv+"/api/v1/agents/ready", map[string]any{"clusterId": cluster, "hostname": host, "ok": true})
}

type setReadyInput struct {
	Trigger        bool   `json:"trigger"`
	CreateExternal bool   `json:"createExternal"`
	CIDR           string `json:"cidr"`
	Gateway        string `json:"gateway"`
	IPRange        string `json:"ipRange"`
}

// pollSetReady asks the server for the operator's set_ready input (submitted via
// the UI once all nodes are configured).
func pollSetReady(srv, cluster string) (setReadyInput, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv+"/api/v1/clusters/"+cluster+"/set-ready", nil)
	if err != nil {
		return setReadyInput{}, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return setReadyInput{}, false
	}
	defer resp.Body.Close()
	var out setReadyInput
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return setReadyInput{}, false
	}
	return out, true
}

// postJSON fires a best-effort JSON POST (used for ready/report signals).
func postJSON(url string, payload any) {
	body, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
}

// reportApplied posts apply-complete to the server. For the master this triggers
// the server to write the "go" SEL to the non-masters.
func reportApplied(srv, cluster, host string, isMaster bool) {
	body, _ := json.Marshal(map[string]any{
		"clusterId": cluster, "hostname": host, "isMaster": isMaster,
		"macs": macs(), "serial": serial(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", srv+"/api/v1/agents/applied", bytes.NewReader(body))
	if err != nil {
		log.Printf("reportApplied: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("reportApplied post: %v", err)
		return
	}
	resp.Body.Close()
	log.Printf("reportApplied: HTTP %d", resp.StatusCode)
}

// runInspectCheck does a single quick check-in before the installer fetches the
// image. If the server has marked this machine for inspect, it reports hardware
// inventory and halts — skipping the multi-GB image fetch. Otherwise it returns
// so rc.local proceeds with the normal install. It waits (bounded) for the
// post-DHCP network; if the server stays unreachable it returns and installs.
func runInspectCheck(srv string) {
	body, _ := json.Marshal(map[string]any{"macs": macs(), "serial": serial()})
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		req, err := http.NewRequestWithContext(ctx, "POST", srv+"/api/v1/agents/preflight/checkin", bytes.NewReader(body))
		if err != nil {
			cancel()
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		var out agent.PreflightCheckinResponse
		derr := json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if derr != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		if out.Inspect {
			log.Printf("inspect-check: marked for inspect — inventory + halt (skipping image fetch)")
			reportInventory(srv)
			return
		}
		log.Printf("inspect-check: normal install (appointed=%v) — proceeding to fetch", out.Appointed)
		return
	}
	log.Printf("inspect-check: server unreachable within timeout — proceeding to install")
}

// runPreflight runs the installer-phase network validation and blocks until
// green light 1 clears (then the installer proceeds to restore).
func runPreflight(srv string, poll time.Duration) {
	deps := agent.PreflightDeps{
		Identity:          func() ([]string, string) { return macs(), serial() },
		SetClock:          setClock,
		Now:               time.Now,
		Configured:        func() bool { _, err := os.Stat(configuredMarker); return err == nil },
		ConfigureTopology: configureTopology,
		FetchSnapshot:     fetchSnapshot,
		StampEnv:          stampEnv,
		Carrier:           carrier,
		Probe:             probe,
		WriteSEL:          writeSEL,
		HTTP:              &http.Client{Timeout: 30 * time.Second},
		Sleep:             time.Sleep,
	}
	log.Printf("phone-home-agent --preflight: server=%s", srv)
	if err := agent.RunPreflight(context.Background(), srv, deps, poll); err != nil {
		if errors.Is(err, agent.ErrInspect) {
			log.Printf("phone-home-agent --preflight: inspect boot — reporting hardware inventory")
			reportInventory(srv)
			return
		}
		log.Fatalf("preflight: %v", err)
	}
	log.Printf("phone-home-agent --preflight: green light 1 — proceeding to restore")
}
