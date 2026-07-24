// Package api provides the HTTP server: REST API + embedded SPA.
package api

import "net/http"

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
	return mux
}
