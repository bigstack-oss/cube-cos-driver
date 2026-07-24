// Package api provides the HTTP server: REST API + embedded SPA.
package api

import (
	"net/http"

	"github.com/bigstack-oss/cube-cos-snapshot/internal/storage"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/webui"
)

type Config struct {
	DataDir   string
	ExportDir string
}

func New(cfg Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	h := &handlers{store: &storage.Store{DataDir: cfg.DataDir, ExportDir: cfg.ExportDir}}
	h.register(mux)
	mux.Handle("/", webui.Handler())
	return mux
}
