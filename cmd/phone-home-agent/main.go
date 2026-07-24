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
		log.Fatalf("no snapshot server (set --server, SNAPSHOT_SERVER, or snapshot_server= on the kernel cmdline)")
	}

	deps := agent.Deps{
		Identity: func() ([]string, string) { return macs(), serial() },
		SetClock: setClock,
		Probe:    probe,
		Apply:    apply,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		Now:      time.Now,
		Sleep:    time.Sleep,
	}
	log.Printf("phone-home-agent: server=%s", srv)
	if err := agent.Run(context.Background(), srv, deps, *poll); err != nil {
		log.Fatalf("agent: %v", err)
	}
	log.Printf("phone-home-agent: snapshot applied")
}
