// phone-home-agent runs on first boot of an unconfigured CubeCOS node. It
// phones home to the snapshot server, syncs its clock, runs a network
// preflight, and pulls + applies its appointed snapshot. Ships inside the
// CubeCOS image; launched by a systemd unit gated on the node being
// unconfigured.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bigstack-oss/cube-cos-snapshot/internal/agent"
)

const configuredMarker = "/etc/appliance/state/configured"

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// serverFromCmdline reads snapshot_server=<url> from /proc/cmdline, if present.
func serverFromCmdline() string {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return ""
	}
	for _, tok := range strings.Fields(string(data)) {
		if v, ok := strings.CutPrefix(tok, "snapshot_server="); ok {
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

func apply(ctx context.Context, snapshotURL string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", snapshotURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", snapshotURL, resp.StatusCode)
	}
	dest := "/var/support/appointed.snapshot"
	_ = os.MkdirAll(filepath.Dir(dest), 0o755)
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	f.Close()
	// The documented CLI apply path.
	out, err := exec.CommandContext(ctx, "hex_config", "snapshot_apply", dest).CombinedOutput()
	if err != nil {
		return fmt.Errorf("snapshot_apply: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func main() {
	server := flag.String("server", envOr("SNAPSHOT_SERVER", ""), "snapshot server URL (default: snapshot_server= from kernel cmdline)")
	poll := flag.Duration("poll", 15*time.Second, "check-in poll interval")
	preflight := flag.Bool("preflight", false, "installer-phase network preflight (pre-restore); blocks until green light 1")
	flag.Parse()

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

	if *preflight {
		runPreflight(srv, *poll)
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
	log.Printf("phone-home-agent: server=%s", srv)
	if err := agent.Run(context.Background(), srv, deps, *poll); err != nil {
		log.Fatalf("agent: %v", err)
	}
	log.Printf("phone-home-agent: snapshot applied")
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
		Carrier:           carrier,
		Probe:             probe,
		WriteSEL:          writeSEL,
		HTTP:              &http.Client{Timeout: 30 * time.Second},
		Sleep:             time.Sleep,
	}
	log.Printf("phone-home-agent --preflight: server=%s", srv)
	if err := agent.RunPreflight(context.Background(), srv, deps, poll); err != nil {
		log.Fatalf("preflight: %v", err)
	}
	log.Printf("phone-home-agent --preflight: green light 1 — proceeding to restore")
}
