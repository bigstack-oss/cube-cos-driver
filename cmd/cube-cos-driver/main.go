package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/bigstack-oss/cube-cos-driver/internal/api"
	"github.com/bigstack-oss/cube-cos-driver/internal/orchestrator"
)

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
		DataDir:       *dataDir,
		ExportDir:     *exportDir,
		SecretKey:     os.Getenv("SNAPSHOT_SECRET_KEY"),
		SecretKeyFile: *secretKeyFile,
	}
	if *simulate {
		cfg.DeployExecutor = orchestrator.NewFakeExecutor()
		cfg.DeployConfig = orchestrator.Config{PollInterval: 800 * time.Millisecond, StageTimeout: time.Minute, PowerStagger: 5 * time.Second}
		log.Printf("deploy simulation mode: using fake executor (no real IPMI)")
	}
	handler, err := api.New(cfg)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	log.Printf("cube-cos-driver listening on :%s (data-dir=%s export-dir=%s)", *port, *dataDir, *exportDir)
	if err := http.ListenAndServe(":"+*port, handler); err != nil {
		log.Fatal(err)
	}
}
