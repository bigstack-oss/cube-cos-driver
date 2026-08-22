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

// advisorInstallScriptName is the on-disk (and pushed) name of the Cube AI
// Advisor end-to-end install script.
const advisorInstallScriptName = "cube-advisor-install.sh"
const advisorUninstallScriptName = "cube-advisor-uninstall.sh"

//go:embed assets/install-portal.sh
var installPortalScript string

//go:embed assets/uninstall-portal.sh
var uninstallPortalScript string

//go:embed assets/install-advisor.sh
var installAdvisorScript string

//go:embed assets/uninstall-advisor.sh
var uninstallAdvisorScript string

// materializePortalScript writes the embedded portal install + uninstall scripts
// into the datadir so the cmp plans can push them to the cluster like any other
// artifact.
func materializePortalScript(root string) {
	dir := filepath.Join(root, "cubecmp")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, portalInstallScriptName), []byte(installPortalScript), 0o755)
	_ = os.WriteFile(filepath.Join(dir, portalUninstallScriptName), []byte(uninstallPortalScript), 0o755)
}

// materializeAdvisorScript writes the embedded advisor install + uninstall
// scripts into the datadir so the advisor plans can push them to the cluster
// like any other artifact.
func materializeAdvisorScript(root string) {
	dir := filepath.Join(root, "advisor")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, advisorInstallScriptName), []byte(installAdvisorScript), 0o755)
	_ = os.WriteFile(filepath.Join(dir, advisorUninstallScriptName), []byte(uninstallAdvisorScript), 0o755)
}
