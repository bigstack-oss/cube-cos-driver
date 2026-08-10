package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/bigstack-oss/cube-cos-driver/internal/api"
	"github.com/bigstack-oss/cube-cos-driver/internal/discovery"
	"github.com/bigstack-oss/cube-cos-driver/internal/orchestrator"
)

// version is stamped at build time via -ldflags "-X main.version=…" (see the
// Makefile). Defaults to "dev" for a plain `go build`.
var version = "dev"

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	port := flag.String("port", envOr("PORT", "3001"), "listen port")
	dataDir := flag.String("data-dir", envOr("DATA_DIR", "./storage/snapshot"), "cluster snapshot store")
	exportDir := flag.String("export-dir", envOr("EXPORT_DIR", ""), "flat .snapshot export dir (pxeserver: /var/ftpboot)")
	secretKeyFile := flag.String("secret-key-file", envOr("SECRET_KEY_FILE", ""), "file holding the AES key for BMC passwords (default: <data-dir>/.secret-key)")
	simulate := flag.Bool("simulate", envOr("SIMULATE", "") != "", "simulate deploys with a fake executor (no real IPMI) — for demos/dev")
	advertise := flag.String("advertise", envOr("ADVERTISE", ""), "node-reachable driver endpoint ip:port stamped into node BMC SEL (e.g. 10.32.0.202:80); empty = don't stamp")
	pxeRoot := flag.String("pxe-root", envOr("PXE_ROOT", ""), "PXE server grub.cfg dir (local or NFS mount) enabling the image picker + default flip; empty = disabled")
	agentBin := flag.String("agent-binary", envOr("AGENT_BINARY", ""), "packed phone-home-agent served for installer hot-update; empty = <driver-dir>/phone-home-agent if present")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	if *exportDir != "" {
		if err := os.MkdirAll(*exportDir, 0o755); err != nil {
			log.Fatalf("export dir: %v", err)
		}
	}

	cfg := api.Config{
		Version:       version,
		DataDir:       *dataDir,
		ExportDir:     *exportDir,
		SecretKey:     os.Getenv("SNAPSHOT_SECRET_KEY"),
		SecretKeyFile: *secretKeyFile,
		Advertise:     *advertise,
		PXERoot:       *pxeRoot,
		AgentBinPath:  *agentBin,
	}
	if *simulate {
		cfg.DeployExecutor = orchestrator.NewFakeExecutor()
		cfg.DeployConfig = orchestrator.Config{PollInterval: 800 * time.Millisecond, StageTimeout: time.Minute, PowerStagger: 5 * time.Second}
		cfg.Discoverer = discovery.Simulated{}
		log.Printf("deploy simulation mode: using fake executor + simulated BMC discovery (no real IPMI)")
	}
	handler, err := api.New(cfg)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	log.Printf("cube-cos-driver %s listening on :%s (data-dir=%s export-dir=%s)", version, *port, *dataDir, *exportDir)
	if err := http.ListenAndServe(":"+*port, handler); err != nil {
		log.Fatal(err)
	}
}
