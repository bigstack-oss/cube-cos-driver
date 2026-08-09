package enterprise

import (
	_ "embed"
	"os"
	"path/filepath"
)

// portalInstallScriptName is the on-disk (and pushed) name of the CMP portal
// end-to-end install script.
const portalInstallScriptName = "cube-portal-install.sh"

//go:embed assets/install-portal.sh
var installPortalScript string

// materializePortalScript writes the embedded portal-install script into the
// datadir so the cmp plan can push it to the cluster like any other artifact.
func materializePortalScript(dataDir string) {
	dir := filepath.Join(dataDir, "enterprise", "cubecmp")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, portalInstallScriptName), []byte(installPortalScript), 0o755)
}
