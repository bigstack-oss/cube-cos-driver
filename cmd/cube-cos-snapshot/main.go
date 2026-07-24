package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/bigstack-oss/cube-cos-snapshot/internal/api"
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
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	if *exportDir != "" {
		if err := os.MkdirAll(*exportDir, 0o755); err != nil {
			log.Fatalf("export dir: %v", err)
		}
	}

	handler := api.New(api.Config{DataDir: *dataDir, ExportDir: *exportDir})
	log.Printf("cube-cos-snapshot listening on :%s (data-dir=%s export-dir=%s)", *port, *dataDir, *exportDir)
	if err := http.ListenAndServe(":"+*port, handler); err != nil {
		log.Fatal(err)
	}
}
