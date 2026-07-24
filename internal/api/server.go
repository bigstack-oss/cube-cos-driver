// Package api provides the HTTP server: REST API + embedded SPA.
package api

import (
	"net/http"
	"path/filepath"

	"github.com/bigstack-oss/cube-cos-snapshot/internal/discovery"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/inventory"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/orchestrator"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/secret"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/storage"
	"github.com/bigstack-oss/cube-cos-snapshot/internal/webui"
)

type Config struct {
	DataDir   string
	ExportDir string
	// SecretKey / SecretKeyFile locate the AES key for BMC passwords.
	// If both are empty, a key file is auto-generated under DataDir.
	SecretKey     string
	SecretKeyFile string
	// Discoverer overrides the hardware-discovery backend (tests inject a
	// fake). Defaults to Redfish-first + IPMI fallback.
	Discoverer discovery.Discoverer
	// DeployExecutor overrides the deploy hardware backend (tests inject a
	// fake). Defaults to the real IPMI executor + pxeserver observer.
	DeployExecutor orchestrator.Executor
	// DeployConfig tunes deploy timing (tests use small values).
	DeployConfig orchestrator.Config
}

func New(cfg Config) (http.Handler, error) {
	keyFile := cfg.SecretKeyFile
	if keyFile == "" {
		keyFile = filepath.Join(cfg.DataDir, ".secret-key")
	}
	box, err := secret.Load(cfg.SecretKey, keyFile)
	if err != nil {
		return nil, err
	}
	machineStore, err := inventory.NewStore(filepath.Join(cfg.DataDir, "machines"), box)
	if err != nil {
		return nil, err
	}
	discoverer := cfg.Discoverer
	if discoverer == nil {
		discoverer = discovery.Default()
	}

	clusterStore := &storage.Store{DataDir: cfg.DataDir, ExportDir: cfg.ExportDir}

	deployExec := cfg.DeployExecutor
	if deployExec == nil {
		deployExec = orchestrator.IPMIExecutor{Observer: orchestrator.DefaultObserver()}
	}
	deployStore, err := orchestrator.NewStore(filepath.Join(cfg.DataDir, "deploys"))
	if err != nil {
		return nil, err
	}
	mgr := orchestrator.NewManager(deployStore, deployExec, cfg.DeployConfig)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	h := &handlers{store: clusterStore, machines: machineStore}
	h.register(mux)
	mh := &machineHandlers{store: machineStore, discoverer: discoverer}
	mh.register(mux)
	dh := &deployHandlers{clusters: clusterStore, machines: machineStore, mgr: mgr}
	dh.register(mux)
	mux.Handle("/", webui.Handler())
	return mux, nil
}
