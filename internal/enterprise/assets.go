package enterprise

import (
	_ "embed"
	"os"
	"path/filepath"
)

// portalInstallScriptName is the on-disk (and pushed) name of the CMP portal
// end-to-end install script.
const portalInstallScriptName = "cube-portal-install.sh"
const portalUninstallScriptName = "cube-portal-uninstall.sh"

//go:embed assets/install-portal.sh
var installPortalScript string

//go:embed assets/uninstall-portal.sh
var uninstallPortalScript string

// materializePortalScript writes the embedded portal install + uninstall scripts
// into the datadir so the cmp plans can push them to the cluster like any other
// artifact.
func materializePortalScript(root string) {
	dir := filepath.Join(root, "cubecmp")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, portalInstallScriptName), []byte(installPortalScript), 0o755)
	_ = os.WriteFile(filepath.Join(dir, portalUninstallScriptName), []byte(uninstallPortalScript), 0o755)
}
