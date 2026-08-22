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

// frameworkLBSweep reaps the framework's k8s-created ingress LBs
// (kube_service_<fw>_*) and their floating IPs after framework_delete, which
// deletes the VMs directly and orphans them. %s = framework name.
const frameworkLBSweep = `source /etc/admin-openrc.sh 2>/dev/null
fw=%s
found=0
for lb in $(openstack loadbalancer list -f value -c id -c name 2>/dev/null | awk -v p="kube_service_${fw}_" '$2 ~ ("^" p){print $1}'); do
  found=1
  vip=$(openstack loadbalancer show "$lb" -f value -c vip_address 2>/dev/null)
  for fip in $(openstack floating ip list --fixed-ip-address "$vip" -f value -c ID 2>/dev/null); do
    openstack floating ip delete "$fip" 2>/dev/null && echo "freed floating IP mapped to $vip"
  done
  openstack loadbalancer delete "$lb" --cascade 2>/dev/null && echo "deleted orphaned ingress LB $lb"
done
[ "$found" = 0 ] && echo "no orphaned kube_service_${fw}_* load balancers to reap"
`

// advisorLBSweep reaps ONLY the advisor Service's Octavia LB (+ its floating
// IPs) after uninstall_advisor — the cloud-provider cannot delete an LB stuck
// in provisioning ERROR, which otherwise pins the Service finalizer and the
// cube-advisor namespace in Terminating. Scoped to the exact service name so
// the framework ingress LB is never touched. %s = framework name.
const advisorLBSweep = `source /etc/admin-openrc.sh 2>/dev/null
fw=%s
found=0
for lb in $(openstack loadbalancer list -f value -c id -c name 2>/dev/null | awk -v n="kube_service_${fw}_cube-advisor_cube-advisor" '$2 == n {print $1}'); do
  found=1
  vip=$(openstack loadbalancer show "$lb" -f value -c vip_address 2>/dev/null)
  for fip in $(openstack floating ip list --fixed-ip-address "$vip" -f value -c ID 2>/dev/null); do
    openstack floating ip delete "$fip" 2>/dev/null && echo "freed floating IP mapped to $vip"
  done
  openstack loadbalancer delete "$lb" --cascade 2>/dev/null && echo "deleted advisor LB $lb"
done
[ "$found" = 0 ] && echo "no advisor load balancer to reap"
exit 0
`

// localPath builds the path to a bundled artifact under the enterprise images
// folder (root/<sub>/<file>). root is the configurable enterprise dir.
func localPath(root, sub, file string) string {
	return filepath.Join(root, sub, file)
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
			// Skip cleanly (don't dump hex_sdk usage) on images without the
			// airgap_sim_apply function instead of running a no-op.
			Cmd: "cubectl node exec -p 'if grep -rqw airgap_sim_apply /usr/lib/hex_sdk/modules/; then hex_sdk airgap_sim_apply; else echo \"airgap_sim_apply not on this image — air-gap simulation skipped\"; fi'",
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

	if module == ModuleAdvisor {
		// chart version derived from the .pigz name: cube-advisor-<ver>.pigz.
		chartVer := strings.TrimSuffix(strings.TrimPrefix(p.AdvisorFile, "cube-advisor-"), ".pigz")
		steps = append(steps,
			plannedStep{
				Name:  "advisor_register",
				Title: "Register Cube AI Advisor application",
				Kind:  "scp+run",
				// hex_sdk app_import directly, NOT hex_cli app_register: the CLI
				// wrapper exits 0 and prints only "app failed to install" on
				// failure, so a failed import.sh (chart never pushed) would pass
				// this step and only surface as a cryptic install_advisor error.
				Cmd:        fmt.Sprintf("hex_sdk app_import %s/%s %s skip_flavor", cephfsUpdate, p.AdvisorFile, p.Framework),
				LocalPath:  localPath(dataDir, "advisor", p.AdvisorFile),
				RemotePath: cephfsUpdate,
			},
			// advisor_register only pushes the chart + prereqs; this deploys the
			// advisor end-to-end (helm install, rollout waits) and self-verifies
			// that it serves.
			plannedStep{
				Name:       "install_advisor",
				Title:      "Deploy + verify Cube AI Advisor",
				Kind:       "scp+run",
				Cmd:        fmt.Sprintf("bash /tmp/%s %s %s %s", advisorInstallScriptName, p.Project, p.AdvisorLBIP, chartVer),
				LocalPath:  localPath(dataDir, "advisor", advisorInstallScriptName),
				RemotePath: "/tmp",
			},
			plannedStep{Name: "complete", Title: "Installation complete", Kind: "complete"},
		)
	}

	return steps
}

// BuildUninstallPlan returns the ordered steps to tear a module down.
//   - cmp:     helm-uninstall the portal + delete its namespace (App-Framework,
//     pushed chart, and imported images are left in place).
//   - advisor: helm-uninstall the advisor + delete its namespace (same shape
//     as cmp).
//   - appfw:   framework_delete, which removes the framework and every app on
//     it (CubeCMP and Advisor are apps on it).
func BuildUninstallPlan(module string, p InstallParams, dataDir string) []plannedStep {
	steps := []plannedStep{
		{Name: "preflight", Title: "Preflight checks", Kind: "detect"},
	}
	switch module {
	case ModuleCMP:
		fw := p.Framework
		if fw == "" {
			fw = p.Project
		}
		steps = append(steps, plannedStep{
			Name:       "uninstall_portal",
			Title:      "Uninstall CubeCMP portal",
			Kind:       "scp+run",
			Cmd:        fmt.Sprintf("bash /tmp/%s %s", portalUninstallScriptName, fw),
			LocalPath:  localPath(dataDir, "cubecmp", portalUninstallScriptName),
			RemotePath: "/tmp",
		})
	case ModuleAdvisor:
		fw := p.Framework
		if fw == "" {
			fw = p.Project
		}
		steps = append(steps,
			plannedStep{
				Name:       "uninstall_advisor",
				Title:      "Uninstall Cube AI Advisor",
				Kind:       "scp+run",
				Cmd:        fmt.Sprintf("bash /tmp/%s %s", advisorUninstallScriptName, fw),
				LocalPath:  localPath(dataDir, "advisor", advisorUninstallScriptName),
				RemotePath: "/tmp",
			},
			// The in-cluster cloud-provider refuses to delete a non-ACTIVE LB
			// ("is not ACTIVE, current provisioning status: ERROR"), so an
			// advisor Service whose Octavia LB errored blocks its own finalizer
			// and leaves the namespace Terminating forever. Reap only THIS
			// service's LB (never the framework ingress); occm's next retry
			// sees 404, treats it as deleted, and releases the finalizer.
			plannedStep{
				Name:  "advisor_lb_sweep",
				Title: "Reap the advisor's leaked load balancer",
				Kind:  "run",
				Cmd:   fmt.Sprintf(advisorLBSweep, fw),
			},
		)
	case ModuleAppFW:
		fw := p.Project
		if fw == "" {
			fw = p.Framework
		}
		steps = append(steps,
			plannedStep{
				Name:  "framework_delete",
				Title: "Delete app framework (removes all apps on it)",
				Kind:  "run",
				Cmd:   fmt.Sprintf("hex_cli -c app -c framework_delete %s", fw),
			},
			// framework_delete tears down the rke2 VMs directly, so the in-cluster
			// k8s cloud-provider never reaps the ingress LBs it created — they
			// orphan and hold their LB IP. Sweep them (+ their floating IPs) here.
			plannedStep{
				Name:  "lb_sweep",
				Title: "Reap orphaned ingress load balancers",
				Kind:  "run",
				Cmd:   fmt.Sprintf(frameworkLBSweep, fw),
			},
		)
	}
	steps = append(steps, plannedStep{Name: "complete", Title: "Uninstall complete", Kind: "complete"})
	return steps
}
