package enterprise

import (
	"os"
	"sort"
	"strings"
)

// Artifacts holds discovered pre-staged artifact basenames.
type Artifacts struct {
	AppFW   []string // rancher .raw images and supporting files
	CMP     []string // cube-portal and related files
	Advisor []string // cube-advisor and related files
}

// DiscoverArtifacts scans the enterprise root for appfw/, cubecmp/, and
// advisor/ and returns basenames. root is the configurable enterprise images
// folder.
func DiscoverArtifacts(root string) Artifacts {
	return Artifacts{
		AppFW:   readDir(root, "appfw"),
		CMP:     readDir(root, "cubecmp"),
		Advisor: readDir(root, "advisor"),
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
