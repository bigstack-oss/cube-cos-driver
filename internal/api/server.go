// Package api provides the HTTP server: REST API + embedded SPA.
package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/bigstack-oss/cube-cos-driver/internal/discovery"
	"github.com/bigstack-oss/cube-cos-driver/internal/inventory"
	"github.com/bigstack-oss/cube-cos-driver/internal/orchestrator"
	"github.com/bigstack-oss/cube-cos-driver/internal/pxe"
	"github.com/bigstack-oss/cube-cos-driver/internal/secret"
	"github.com/bigstack-oss/cube-cos-driver/internal/storage"
	"github.com/bigstack-oss/cube-cos-driver/internal/webui"
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
	// Advertise is the driver's node-reachable "ip:port" stamped into each
	// booting node's BMC SEL so the node phones home to THIS driver regardless
	// of the PXE entry's driver_server=. Empty = don't stamp.
	Advertise string
	// PXERoot is the PXE server's grub.cfg directory (local, or an NFS mount of
	// the PXE export). Enables the deploy/inspect image picker + default flip.
	// Empty = image selection disabled.
	PXERoot string
	// AgentBinPath is the packed phone-home-agent binary the driver serves at
	// /api/v1/agents/binary so the installer can hot-update the OS agent without
	// a full image rebuild. Empty resolves to <driver-dir>/phone-home-agent.
	AgentBinPath string
}

// New builds the HTTP handler. For graceful shutdown / test teardown of the
// deploy manager's background goroutines, use newHandler (returns the manager).
func New(cfg Config) (http.Handler, error) {
	h, _, err := newHandler(cfg)
	return h, err
}

func newHandler(cfg Config) (http.Handler, *orchestrator.Manager, error) {
	keyFile := cfg.SecretKeyFile
	if keyFile == "" {
		keyFile = filepath.Join(cfg.DataDir, ".secret-key")
	}
	box, err := secret.Load(cfg.SecretKey, keyFile)
	if err != nil {
		return nil, nil, err
	}
	machineStore, err := inventory.NewStore(filepath.Join(cfg.DataDir, "machines"), box)
	if err != nil {
		return nil, nil, err
	}
	discoverer := cfg.Discoverer
	if discoverer == nil {
		discoverer = discovery.Default()
	}

	clusterStore := &storage.Store{DataDir: cfg.DataDir, ExportDir: cfg.ExportDir}

	deployExec := cfg.DeployExecutor
	realHW := deployExec == nil
	if realHW {
		deployExec = orchestrator.IPMIExecutor{Observer: orchestrator.DefaultObserver()}
	}
	deployStore, err := orchestrator.NewStore(filepath.Join(cfg.DataDir, "deploys"))
	if err != nil {
		return nil, nil, err
	}
	mgr := orchestrator.NewManager(deployStore, deployExec, cfg.DeployConfig)
	if realHW {
		// Real hardware: read node status out-of-band over the BMC LAN, and
		// release non-masters after the master applies by writing the "go" SEL.
		mgr.SetSELObserver(orchestrator.IPMISELObserver{})
		mgr.SetGateWriter(orchestrator.IPMIGateWriter{})
		if os.Getenv("MANUAL_GATE") == "1" {
			mgr.SetManualGate(true)
		}
	}
	if cfg.Advertise != "" {
		ip, port, err := orchestrator.ParseAdvertise(cfg.Advertise)
		if err != nil {
			return nil, nil, fmt.Errorf("advertise %q: %w", cfg.Advertise, err)
		}
		mgr.SetAdvertise(ip, port)
	}
	if cfg.PXERoot != "" {
		mgr.SetPXEFlipper(pxe.NewFlipper(cfg.PXERoot))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	h := &handlers{store: clusterStore, machines: machineStore, mgr: mgr}
	h.register(mux)
	mh := &machineHandlers{store: machineStore, discoverer: discoverer, mgr: mgr}
	mh.register(mux)
	agentBin := cfg.AgentBinPath
	if agentBin == "" {
		if exe, err := os.Executable(); err == nil {
			agentBin = filepath.Join(filepath.Dir(exe), "phone-home-agent")
		}
	}
	if agentBin != "" {
		if _, err := os.Stat(agentBin); err != nil {
			agentBin = "" // not bundled: hot-update disabled, installer uses baked-in agent
		}
	}
	dh := &deployHandlers{clusters: clusterStore, machines: machineStore, mgr: mgr, pxeRoot: cfg.PXERoot, agentBin: agentBin}
	dh.register(mux)
	mux.Handle("/", webui.Handler())
	return mux, mgr, nil
}
