// Package api provides the HTTP server: REST API + embedded SPA.
package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/bigstack-oss/cube-cos-driver/internal/clusterssh"
	"github.com/bigstack-oss/cube-cos-driver/internal/discovery"
	"github.com/bigstack-oss/cube-cos-driver/internal/enterprise"
	"github.com/bigstack-oss/cube-cos-driver/internal/inventory"
	"github.com/bigstack-oss/cube-cos-driver/internal/orchestrator"
	"github.com/bigstack-oss/cube-cos-driver/internal/pxe"
	"github.com/bigstack-oss/cube-cos-driver/internal/secret"
	"github.com/bigstack-oss/cube-cos-driver/internal/storage"
	"github.com/bigstack-oss/cube-cos-driver/internal/webui"
)

type Config struct {
	// Version is the build stamp (git describe), surfaced at GET /api/v1/version
	// so each running instance reports which build it is.
	Version   string
	DataDir   string
	ExportDir string
	// EnterpriseDir is the folder holding the App-Framework + CubeCMP install
	// images (large; may live on separately-mounted USB/virtual media). Empty
	// defaults to <DataDir>/enterprise. Runtime-overridable from the UI.
	EnterpriseDir string
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
	// EnterpriseDial overrides how the enterprise install manager dials a
	// cluster's VIP (tests inject a mock). Defaults to clusterssh.NewSSHClient.
	EnterpriseDial func(host, user, password string) (clusterssh.Client, error)
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
	version := cfg.Version
	if version == "" {
		version = "dev"
	}
	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": version})
	})
	mux.HandleFunc("GET /api/v1/fs/dirs", fsDirs)
	mux.HandleFunc("GET /api/v1/fs/devices", fsDevices)
	mux.HandleFunc("POST /api/v1/fs/mount", fsMount)
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

	entStore, err := enterprise.NewStore(filepath.Join(cfg.DataDir, "installs"))
	if err != nil {
		return nil, nil, err
	}
	entDial := cfg.EnterpriseDial
	if entDial == nil {
		entDial = func(host, user, password string) (clusterssh.Client, error) {
			return clusterssh.NewSSHClient(host, user, password)
		}
	}
	// The enterprise images folder (appfw + cmp install artifacts) is a runtime
	// setting persisted in DataDir; --enterprise-dir seeds it, defaulting to the
	// in-tree <DataDir>/enterprise so behavior is unchanged when unset.
	entSeed := cfg.EnterpriseDir
	if entSeed == "" {
		entSeed = filepath.Join(cfg.DataDir, "enterprise")
	}
	entDir := enterprise.NewDir(cfg.DataDir, entSeed)
	entMgr := enterprise.NewManager(entStore, entDir, entDial)
	eh := &enterpriseHandlers{clusters: clusterStore, mgr: entMgr, dataDir: cfg.DataDir, dir: entDir, box: box}
	eh.register(mux)

	mux.Handle("/", webui.Handler())
	return mux, mgr, nil
}
