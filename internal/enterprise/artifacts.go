package enterprise

import (
	"os"
	"sort"
	"strings"
)

// Artifacts holds discovered pre-staged artifact basenames.
type Artifacts struct {
	AppFW []string // rancher .raw images and supporting files
	CMP   []string // cube-portal and related files
}

// DiscoverArtifacts scans dataDir for enterprise/{appfw,cubecmp} and returns basenames.
func DiscoverArtifacts(dataDir string) Artifacts {
	return Artifacts{
		AppFW: readDir(dataDir, "enterprise/appfw"),
		CMP:   readDir(dataDir, "enterprise/cubecmp"),
	}
}

// readDir reads a subdirectory, returns sorted basenames, skips dotfiles and subdirs.
func readDir(baseDir, subDir string) []string {
	entries, err := os.ReadDir(strings.Join([]string{baseDir, subDir}, "/"))
	if err != nil {
		return []string{}
	}

	var names []string
	for _, entry := range entries {
		// Skip directories and dotfiles
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		names = append(names, entry.Name())
	}

	sort.Strings(names)
	return names
}
