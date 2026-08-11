package enterprise

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Dir holds the runtime-configurable enterprise artifacts folder — the place
// that holds the App-Framework + CubeCMP install images (the large rancher
// .raw, the qcow2 service images, the cube-portal .pigz). These are big and
// can live on separately-mounted media (USB / virtual media) that is NOT
// shipped with the pxeserver deployment, kept apart from the cluster snapshot
// store (DataDir). The chosen path is persisted in <dataDir>/settings.json;
// --enterprise-dir (default <dataDir>/enterprise) only seeds the initial value.
//
// The folder holds appfw/, cubecmp/, and manifests/ subdirs; the driver also
// materializes the portal install/uninstall scripts under cubecmp/.
type Dir struct {
	mu       sync.RWMutex
	dir      string
	settings string
}

type settingsFile struct {
	EnterpriseDir string `json:"enterpriseDir"`
}

// NewDir loads the persisted enterprise folder, falling back to seed.
func NewDir(dataDir, seed string) *Dir {
	d := &Dir{settings: filepath.Join(dataDir, "settings.json"), dir: seed}
	if s := loadEnterpriseDir(d.settings); s != "" {
		d.dir = s
	}
	return d
}

// Get returns the current enterprise artifacts folder.
func (d *Dir) Get() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.dir
}

// Set validates (the folder must exist), persists, and applies a new path.
func (d *Dir) Set(dir string) error {
	if dir == "" {
		return fmt.Errorf("enterprise image folder is required")
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("enterprise image folder %q is not a directory (is the media mounted?)", dir)
	}
	if err := saveEnterpriseDir(d.settings, dir); err != nil {
		return fmt.Errorf("persist enterprise folder: %w", err)
	}
	d.mu.Lock()
	d.dir = dir
	d.mu.Unlock()
	materializePortalScript(dir)
	return nil
}

// Status reports the folder, whether it's mounted (exists), and how many
// App-Framework and CubeCMP artifacts it holds.
func (d *Dir) Status() (dir string, mounted bool, appfw, cmp int) {
	dir = d.Get()
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return dir, false, 0, 0
	}
	a := DiscoverArtifacts(dir)
	return dir, true, len(a.AppFW), len(a.CMP)
}

func loadEnterpriseDir(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var s settingsFile
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	return s.EnterpriseDir
}

func saveEnterpriseDir(path, dir string) error {
	b, err := json.MarshalIndent(settingsFile{EnterpriseDir: dir}, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
