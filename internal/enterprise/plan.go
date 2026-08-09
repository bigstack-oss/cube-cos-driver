package enterprise

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	cephfsGlance = "/mnt/cephfs/glance"
	cephfsUpdate = "/mnt/cephfs/update"
)

// localPath builds the datadir-relative path to a bundled artifact.
func localPath(dataDir, sub, file string) string {
	return filepath.Join(dataDir, "enterprise", sub, file)
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// imageNameOf strips the extension to derive the glance image name.
func imageNameOf(file string) string {
	return strings.TrimSuffix(file, filepath.Ext(file))
}

// BuildPlan returns the ordered steps for a module. The App-Framework sequence
// is always included and every step is idempotent (imports skip existing images,
// framework_create skips/waits when already active), so CMP shows the full plan
// and only re-does what's missing. airgap prepends the airgap-apply step; m (may
// be nil) carries version-specific settings from the manifest.
func BuildPlan(module string, p InstallParams, airgap bool, dataDir string, m *Manifest) []plannedStep {
	var steps []plannedStep

	// The rancher OS image's glance name (used by import + framework_create).
	osImageName := strings.TrimSuffix(p.OSImage, ".raw")
	tenant, visibility, storage, osName := m.importDefaults()
	// The cluster-reported default storage backend wins over the manifest default.
	if p.StorageBackend != "" {
		storage = p.StorageBackend
	}

	steps = append(steps, plannedStep{Name: "preflight", Title: "Preflight checks", Kind: "detect"})

	if airgap {
		steps = append(steps, plannedStep{
			Name:  "airgap-apply",
			Title: "Apply air-gap simulation",
			Kind:  "airgap",
			Cmd:   "cubectl node exec -p 'hex_sdk airgap_sim_apply'",
		})
	}

	// A refreshed appctl (v3.1.0-rc6+) fixes the expired signing key baked into
	// older images; deploy it before any framework operation when the manifest
	// names one and it's staged.
	if m != nil && m.Appctl != "" {
		if appctl := localPath(dataDir, "appfw", m.Appctl); fileExists(appctl) {
			steps = append(steps, plannedStep{
				Name:       "update_appctl",
				Title:      fmt.Sprintf("Update appctl (%s signing-key fix)", m.Name),
				Kind:       "scp+run",
				Cmd:        "chmod +x /usr/local/bin/appctl && hex_cli -c app -c framework_list",
				LocalPath:  appctl,
				RemotePath: "/usr/local/bin",
			})
		}
	}

	{
		steps = append(steps,
			plannedStep{
				Name:       "import_fs",
				Title:      "Import Manila filesystem image",
				Kind:       "scp+run",
				Cmd:        fmt.Sprintf("hex_cli -c iaas -c image -c import_fs local %s", p.FsImage),
				LocalPath:  localPath(dataDir, "appfw", p.FsImage),
				RemotePath: cephfsGlance,
				// import_fs creates a fixed-name glance image (os_manila_image_import).
				ImageName: "manila-service-image",
			},
			plannedStep{
				Name:       "import_lb",
				Title:      "Import Amphora load balancer image",
				Kind:       "scp+run",
				Cmd:        fmt.Sprintf("hex_cli -c iaas -c image -c import_lb local %s", p.LBImage),
				LocalPath:  localPath(dataDir, "appfw", p.LBImage),
				RemotePath: cephfsGlance,
				// import_lb creates a fixed-name glance image (os_octavia_image_import).
				ImageName: "amphora-x64-haproxy",
			},
			plannedStep{
				Name:  "import",
				Title: "Import Rancher cluster image",
				Kind:  "scp+run",
				// Full import form: local <file> <image_name> <domain> <tenant>
				// <source> <visibility> <storage_backend> <os>. Tenant/visibility/
				// backend/os come from the manifest (defaults admin/public/
				// CubeStorage/Others) — NOT the framework name from p.Project.
				Cmd: fmt.Sprintf("hex_cli -c iaas -c image -c import local %s %s default %s 'from Bigstack' %s %s %s",
					p.OSImage, osImageName, tenant, visibility, storage, osName),
				LocalPath:  localPath(dataDir, "appfw", p.OSImage),
				RemotePath: cephfsGlance,
				ImageName:  osImageName,
			},
			plannedStep{
				Name:      "framework_create",
				Title:     "Create app framework",
				Kind:      "framework",
				Framework: p.Project,
				LBIP:      p.LBIP,
				Cmd: fmt.Sprintf("hex_cli -c app -c framework_create %s %s %s %s %s",
					p.Project, p.PublicNet, p.MgmtNet, p.LBIP, osImageName),
			},
		)
	}

	if module == ModuleCMP {
		// chart version derived from the .pigz name: cube-portal-<ver>.pigz.
		chartVer := strings.TrimSuffix(strings.TrimPrefix(p.AppFile, "cube-portal-"), ".pigz")
		steps = append(steps,
			plannedStep{
				Name:       "app_register",
				Title:      "Register CubeCMP application",
				Kind:       "scp+run",
				Cmd:        fmt.Sprintf("hex_cli -c app -c app_register %s/%s %s skip_flavor", cephfsUpdate, p.AppFile, p.Framework),
				LocalPath:  localPath(dataDir, "cubecmp", p.AppFile),
				RemotePath: cephfsUpdate,
			},
			// app_register only pushes the chart + prereqs; this deploys the portal
			// end-to-end (helm install, DB-migration retry) and self-verifies that
			// it serves and admin permission was granted.
			plannedStep{
				Name:       "install_portal",
				Title:      "Deploy + verify CubeCMP portal",
				Kind:       "scp+run",
				Cmd:        fmt.Sprintf("bash /tmp/%s %s %s %s", portalInstallScriptName, p.Project, p.LBIP, chartVer),
				LocalPath:  localPath(dataDir, "cubecmp", portalInstallScriptName),
				RemotePath: "/tmp",
			},
			plannedStep{Name: "complete", Title: "Installation complete", Kind: "complete"},
		)
	}

	return steps
}
