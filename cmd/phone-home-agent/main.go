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
func stampEnv(host, cluster string, isMaster bool, osDisk string, optOutRepair, simulateAirgap bool) {
	m := "0"
	if isMaster {
		m = "1"
	}
	o := "0"
	if optOutRepair {
		o = "1"
	}
	a := "0"
	if simulateAirgap {
		a = "1"
	}
	content := fmt.Sprintf("CUBE_HOSTNAME=%s\nCUBE_CLUSTER_ID=%s\nIS_MASTER=%s\nCUBE_OS_DISK=%s\nOPT_OUT_REPAIR=%s\nSIMULATE_AIRGAP=%s\n", host, cluster, m, osDisk, o, a)
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

// localSetReady is the operator's set_ready params, pre-fetched during preflight
// (network up) and staged into the rootfs at restore — read locally so the
// master runs set_ready with no route to the driver (mgmt is off the flat L2
// by then). Analogous to localSnapshot.
const localSetReady = "/var/support/set-ready.json"

// hex_config exit status is a bitmask (hex/include/hex/config_module.h):
//
//	1 = EXIT_FAILURE (a component failed — snapshot_apply ROLLS BACK its changes)
//	2 = CONFIG_EXIT_NEED_REBOOT ("caller should reboot the system")
//
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

// waitBootstrapDone blocks until the boot-time hex bootstrap finishes
// (bootstrap.post.sh touches /run/bootstrap_done). snapshot_apply must not
// race it: both translate policies into the same /tmp/settings.apply, so a
// concurrent bootstrap overwrites the snapshot's translation between
// translate and commit — the commit then applies unconfigured settings and
// stamps the configured marker anyway.
func waitBootstrapDone(ctx context.Context) {
	const marker = "/run/bootstrap_done"
	for i := 0; ; i++ {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		if i == 0 {
			log.Printf("apply: waiting for the boot bootstrap to finish (%s) ...", marker)
		}
		if i >= 600 { // ~10 min — proceed rather than park forever
			log.Printf("apply: %s never appeared — proceeding anyway", marker)
			return
		}
		time.Sleep(time.Second)
	}
}

func apply(ctx context.Context, snapshotURL string) error {
	waitBootstrapDone(ctx)
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
	// A single-component timezone segfaults pre-fix images mid-commit — refuse
	// now rather than fail minutes in (legacy_timezone.go).
	if terr := checkLegacyTimezone(versionFile, dest); terr != nil {
		return terr
	}
	// Pre-fix images install the snapshot's state markers before committing, so
	// every module sees an already-configured node and skips first-time setup.
	// Strip them, apply, and stamp them ourselves on success (legacy_markers.go).
	var pendingMarkers map[string][]byte
	if needsLegacyMarkerHandling(versionFile) {
		stripped := dest + ".nomarkers"
		if m, serr := stripMarkers(dest, stripped); serr != nil {
			log.Printf("legacy-markers: strip failed (%v) — applying snapshot unmodified", serr)
		} else if len(m) == 0 {
			log.Printf("legacy-markers: snapshot carries no state markers — applying unmodified")
			_ = os.Remove(stripped)
		} else {
			pendingMarkers = m
			dest = stripped
		}
	}
	out, err := exec.CommandContext(ctx, "hex_config", "snapshot_apply", dest).CombinedOutput()
	if err == nil {
		if serr := stampMarkers("/", pendingMarkers); serr != nil {
			return fmt.Errorf("apply succeeded but stamping state markers failed: %w", serr)
		}
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
		// Markers stay stripped across the reboot: the resumed apply re-runs this
		// path and stamps them once the commit finally succeeds.
		return errApplyReboot
	}
	// Soft-complete: some phases exit non-zero yet FTS set the configured marker.
	if _, e := os.Stat(configuredMarker); e == nil {
		log.Printf("snapshot_apply exit %d but %s exists — treating as success", code, configuredMarker)
		if serr := stampMarkers("/", pendingMarkers); serr != nil {
			log.Printf("legacy-markers: stamping after soft-complete failed: %v", serr)
		}
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

// reportRestoreDone marks the image restore complete (installer, just before
// reboot) and holds until the reboot is authorized. The gate is authoritative
// over SEL (OOB): the installer writes a restore-done status record (pollSEL
// greens the UI) and waits for the driver's gate/go(reboot) on its BMC. The
// HTTP POST is best-effort UI only and never gates — the topology reconfig can
// have severed the in-band route to the driver by now.
func reportRestoreDone(srv string, poll time.Duration) {
	_ = writeSEL("restore-done", "ok", "") // OOB status: restore complete
	// Best-effort HTTP notify (one shot, non-blocking on failure).
	body, _ := json.Marshal(map[string]any{"macs": macs(), "serial": serial()})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	if req, err := http.NewRequestWithContext(ctx, "POST", srv+"/api/v1/agents/restore-done", bytes.NewReader(body)); err == nil {
		req.Header.Set("Content-Type", "application/json")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	cancel()
	log.Printf("restore-done reported — waiting for reboot 'go' via SEL (OOB) ...")
	if !waitSELGate(context.Background(), poll, gateStageReboot) {
		log.Printf("restore-done: reboot gate wait ended without go signal")
		return
	}
	log.Printf("restore-done: reboot authorized (SEL go)")
}

// reportApplyStarted tells the server the OS-phase agent is up (reboot done) and
// reports in — so the deploy UI can flip the reboot cell green — and returns
// whether the driver was reachable and whether it authorizes the apply. The
// distinction matters: an unreachable driver is "unknown", NOT "go" — treating
// it as go (the old fail-open) let the master apply before the operator
// authorized it and left the UI stuck on "rebooting" because the report never
// landed.
func reportApplyStarted(srv string) (reachable, proceed, isMaster, optOutRepair bool) {
	body, _ := json.Marshal(map[string]any{"macs": macs(), "serial": serial()})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", srv+"/api/v1/agents/apply-started", bytes.NewReader(body))
	if err != nil {
		return false, false, false, false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, false, false, false // server unreachable
	}
	var out struct {
		OK           bool `json:"ok"`
		Proceed      bool `json:"proceed"`
		IsMaster     bool `json:"isMaster"`
		OptOutRepair bool `json:"optOutRepair"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	return true, out.Proceed, out.IsMaster, out.OptOutRepair
}

// repairOptOutMarker is the persistent, per-slot marker honored by cubecos's
// cluster_check_repair_async + health auto_repair (sdk_health.sh / proj_functions).
// Persistent (not /run) so the opt-out holds across the OS-phase reboots and
// rolling-restart/powercycle iteration; cleared on reimage or by a normal deploy.
const repairOptOutMarker = "/etc/appliance/state/cube_repair_optout"

// setRepairOptOut drops (on) or clears (off) the hidden repair opt-out marker, so
// a normal (non-opt-out) deploy re-enables repair. Best-effort.
func setRepairOptOut(on bool) {
	if on {
		if f, err := os.OpenFile(repairOptOutMarker, os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			f.Close()
			log.Printf("local-apply: repair opt-out ON (%s) — cluster repair + auto_repair disabled this slot", repairOptOutMarker)
		} else {
			log.Printf("local-apply: repair opt-out: create %s failed: %v", repairOptOutMarker, err)
		}
		return
	}
	if err := os.Remove(repairOptOutMarker); err == nil {
		log.Printf("local-apply: repair opt-out OFF — cleared %s", repairOptOutMarker)
	} else if !os.IsNotExist(err) {
		log.Printf("local-apply: repair opt-out: clear %s failed: %v", repairOptOutMarker, err)
	}
}

// startAirgapSim, when the deploy opted into air-gap simulation, applies and
// then re-applies (on a loop) cubecos's CUBE_AIRGAP egress block so any install
// step reaching the internet fails. It re-applies because snapshot apply /
// set_ready reconfigure the network and can flush the chain; the block is left
// in place afterwards (cleared with `cubectl exec -p 'hex_sdk airgap_sim_clear'`).
func startAirgapSim() {
	if os.Getenv("SIMULATE_AIRGAP") != "1" {
		return
	}
	apply := func() {
		if out, err := exec.Command("hex_sdk", "airgap_sim_apply").CombinedOutput(); err != nil {
			log.Printf("local-apply: airgap_sim_apply failed: %v: %s", err, out)
		}
	}
	log.Printf("local-apply: air-gap simulation ON — applying + holding CUBE_AIRGAP egress block")
	apply()
	go func() {
		for {
			time.Sleep(5 * time.Second)
			apply()
		}
	}()
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
	showReport := flag.Bool("report", false, "print this node's latest preflight report (works offline — for console/ssh when the driver is unreachable)")
	flag.Parse()
	if *showReport {
		printPreflightReport()
		return
	}
	setupLogging(*preflight)

	// Already configured → nothing to do.
	if _, err := os.Stat(configuredMarker); err == nil {
		log.Printf("node already configured; exiting")
		return
	}

	// Driver-server precedence: the endpoint the booting driver stamped into our
	// BMC SEL wins over everything — it is the ground truth of which driver
	// powered THIS boot, so a node inspected/deployed by a specific driver
	// phones home to THAT driver regardless of the shared PXE entry's
	// driver_server= (which hex_autoinstall passes as --server). Then --server,
	// then the cmdline, then the pxeserver default.
	srv := ""
	if ep := driverEndpointFromSEL(); ep != "" {
		log.Printf("using driver endpoint from BMC SEL: %s", ep)
		srv = ep
	}
	if srv == "" {
		srv = *server
	}
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
		reportRestoreDone(srv, *poll)
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

	// Hidden repair opt-out (persistent, per-slot): the flag is stamped into the
	// rootfs env at preflight (reliable on the flat L2), NOT fetched over OS-phase
	// HTTP (which can be unreachable before apply configures the network). Set/
	// clear the marker early so cluster_check_repair_async + auto_repair honor it.
	setRepairOptOut(os.Getenv("OPT_OUT_REPAIR") == "1")

	// Hidden air-gap simulation (same rootfs-env stamping): apply + hold the
	// CUBE_AIRGAP egress block for the whole install so any online step fails.
	startAirgapSim()

	// A prior boot already reached a terminal failure — never re-apply a genuine
	// failure. Re-report it (in case the server missed it) and stop.
	if _, e := os.Stat(applyTerminalFile); e == nil {
		log.Printf("local-apply: terminal apply state already reached — not re-applying")
		reportApplyFailed(srv, cluster, host, "apply previously failed (terminal state)")
		return
	}

	// OS phase has no in-band network until apply configures it, so the driver
	// learns we rebooted (and greens the reboot cell) via this OOB SEL record,
	// which pollSEL reads over the BMC. The HTTP reports below are best-effort UI.
	_ = writeSEL("rebooted", "ok", host)

	// The apply gate is authoritative over SEL (OOB): the driver writes a
	// gate/go(apply) record to this node's BMC when the operator authorizes the
	// apply step — the master at apply-master, peers at apply-rest (the driver
	// only releases peers after the master is done, so master-first ordering is
	// preserved driver-side). Auto mode writes the go immediately. HTTP is never
	// consulted for the gate.
	// Resolve master identity authoritatively from the driver when reachable —
	// the IS_MASTER env can be lost in the fragile install-time persistence chain,
	// which would otherwise strand a sole master waiting for itself. This call
	// also flips the driver-side UI state (master → apply-active, peer → wait for
	// master). The env is the offline fallback.
	if reachable, _, driverIsMaster, _ := reportApplyStarted(srv); reachable {
		if driverIsMaster != isMaster {
			log.Printf("local-apply: driver says master=%v (env IS_MASTER=%v) — trusting driver", driverIsMaster, isMaster)
		}
		isMaster = driverIsMaster
	}
	if isMaster {
		log.Printf("local-apply: master — waiting for apply 'go' via SEL (OOB) ...")
	} else {
		log.Printf("local-apply: non-master — waiting for apply 'go' via SEL (OOB) ...")
	}
	if !waitSELGate(ctx, poll, gateStageApply) {
		log.Printf("local-apply: gate wait ended without apply go signal")
		return
	}
	log.Printf("local-apply: apply authorized (SEL go) — applying local snapshot")
	// Mark applying OOB so the driver's pollSEL flips the node to "applying"
	// during the (multi-minute) apply — pre-apply there's no network to report
	// it in-band, so without this the UI sits on "waiting" the whole time.
	_ = writeSEL("applying", "ok", host)

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
		runSetReady(ctx, srv, cluster, host, poll)
	}
}

// runSetReady runs the one-time cluster set_ready on the master. Offline-first:
// the params were pre-fetched during preflight and staged locally, so we read
// them from disk and gate on the set-ready 'go' SEL (OOB) — the master's mgmt
// network is off the flat L2 by now, so we must not depend on the driver. Only
// when the local params are absent do we fall back to polling the driver (best
// effort, for a deploy that didn't pre-stage them).
func runSetReady(ctx context.Context, srv, cluster, host string, poll time.Duration) {
	var sr setReadyInput
	if b, err := os.ReadFile(localSetReady); err == nil && json.Unmarshal(b, &sr) == nil {
		log.Printf("set_ready: using locally-staged params — waiting for set-ready 'go' via SEL (OOB) ...")
		if !waitSELGate(ctx, poll, gateStageSetReady) {
			log.Printf("set_ready: gate wait ended without go signal")
			return
		}
		log.Printf("set_ready: authorized (SEL go)")
	} else {
		log.Printf("set_ready: no local params — falling back to driver poll (network) ...")
		for {
			if s, ok := pollSetReady(srv, cluster); ok && s.Trigger {
				sr = s
				break
			}
			time.Sleep(15 * time.Second)
		}
	}
	args := []string{"-c", "cluster", "-c", "set_ready"}
	// set_ready treats any positional args as "create the shared external
	// network" (argc>1 => it skips the prompt and builds a flat external net
	// with these params). So only pass the network params when the operator
	// actually wants that network; otherwise pass none and decline the prompt
	// so set_ready finalizes without one.
	if sr.CreateExternal {
		for _, a := range []string{sr.CIDR, sr.Gateway, sr.IPRange} {
			if a != "" {
				args = append(args, a)
			}
		}
	}
	log.Printf("set_ready: running hex_cli %v (createExternal=%v)", args, sr.CreateExternal)
	cmd := exec.CommandContext(ctx, "hex_cli", args...)
	// With no args the CLI prompts "Create a shared external network?" — answer
	// per the operator's intent. (With args the prompt is skipped.)
	if sr.CreateExternal {
		cmd.Stdin = strings.NewReader("YES\n")
	} else {
		cmd.Stdin = strings.NewReader("no\n")
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

// fetchSetReadyParams pre-fetches the operator's set_ready params from the
// driver during preflight (network up) and stages them to /run/set-ready.json
// for hex_autoinstall to carry into the rootfs, so the master runs set_ready
// offline post-apply. Best-effort: absent/empty params are skipped (not staged),
// and never fail preflight — the OS agent falls back to the network poll.
func fetchSetReadyParams(srv, clusterID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv+"/api/v1/clusters/"+clusterID+"/set-ready", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var sr setReadyInput
	if json.Unmarshal(b, &sr) != nil {
		return nil // nothing usable yet
	}
	if sr.CIDR == "" && sr.Gateway == "" && sr.IPRange == "" {
		return nil // operator hasn't submitted params yet — don't stage an empty file
	}
	return os.WriteFile("/run/set-ready.json", b, 0o600)
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
		CarrierChecks:     carrierChecks,
		SaveReport:        savePreflightReport,
		Probe:             probe,
		WriteSEL:          writeSEL,
		WaitGate:          func() bool { return selGatePresent(gateStageRestore) },
		FetchSetReady:     func(clusterID string) error { return fetchSetReadyParams(srv, clusterID) },
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

// ---- local preflight report (offline diagnostics) ----

// preflightReportFile persists the latest full preflight round so an operator
// can read it on the node (`phone-home-agent --report`) when the driver is
// unreachable — e.g. the topology reconfig severed the route.
const preflightReportFile = "/run/preflight-report.json"

func savePreflightReport(r agent.PreflightReportRequest) {
	type saved struct {
		agent.PreflightReportRequest
		SavedAt string `json:"savedAt"`
	}
	b, err := json.MarshalIndent(saved{r, time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	if err != nil {
		return
	}
	tmp := preflightReportFile + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, preflightReportFile)
	}
}

// printPreflightReport renders the persisted report human-readable: problems
// first (highlighted), then every check row.
func printPreflightReport() {
	data, err := os.ReadFile(preflightReportFile)
	if err != nil {
		fmt.Printf("no preflight report at %s — preflight has not run (or this node was never appointed).\n", preflightReportFile)
		fmt.Println("see /run/autoinstall.log for the installer progress log.")
		return
	}
	var r struct {
		agent.PreflightReportRequest
		SavedAt string `json:"savedAt"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		fmt.Printf("unreadable report (%v); raw contents:\n%s\n", err, data)
		return
	}
	red, green, bold, reset := "\033[31m", "\033[32m", "\033[1m", "\033[0m"
	verdict := red + "NOT PASSED" + reset
	if r.Passed {
		verdict = green + "PASSED" + reset
	}
	fmt.Printf("%sPREFLIGHT REPORT%s  node %s  cluster %s  saved %s  %s\n",
		bold, reset, r.Hostname, r.ClusterID, r.SavedAt, verdict)
	fmt.Printf("clock skew vs driver: %+.2fs   carrier: %v\n\n", r.ClockSkewSec, r.CarrierOK)
	var fails []agent.PreflightResult
	for _, m := range r.Matrix {
		if !m.OK {
			fails = append(fails, m)
		}
	}
	if len(fails) > 0 {
		fmt.Printf("%s%sPROBLEMS (%d):%s\n", bold, red, len(fails), reset)
		for _, m := range fails {
			fmt.Printf("  %s✗ %s%s", red, m.Target, reset)
			if m.Detail != "" {
				fmt.Printf(" — %s", m.Detail)
			}
			fmt.Println()
		}
		fmt.Println()
	}
	fmt.Printf("%sALL CHECKS (%d):%s\n", bold, len(r.Matrix), reset)
	for _, m := range r.Matrix {
		mark, color := "✓", green
		if !m.OK {
			mark, color = "✗", red
		}
		fmt.Printf("  %s%s%s %s", color, mark, reset, m.Target)
		if m.Detail != "" {
			fmt.Printf(" — %s", m.Detail)
		}
		fmt.Println()
	}
}
