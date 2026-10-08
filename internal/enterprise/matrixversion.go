package enterprise

import (
	"fmt"
	"regexp"
)

var artifactVersionRE = map[string]*regexp.Regexp{
	ModuleCMP:     regexp.MustCompile(`^cube-portal-(\d+\.\d+\.\d+)(?:[+-][^/]*)?\.pigz$`),
	ModuleAdvisor: regexp.MustCompile(`^cube-advisor-(\d+\.\d+\.\d+)(?:[+-][^/]*)?\.pigz$`),
}

// ArtifactVersion reads MAJOR.MINOR.PATCH from a module artifact name, build tag dropped.
func ArtifactVersion(module, file string) (string, error) {
	re, ok := artifactVersionRE[module]
	if !ok {
		return "", fmt.Errorf("module %q has no versioned artifact", module)
	}
	m := re.FindStringSubmatch(file)
	if m == nil {
		return "", fmt.Errorf("can't read a %s version from %q", module, file)
	}
	return m[1], nil
}

// Constrains reports whether the manifest lists any versions for module.
func (m *Manifest) Constrains(module string) bool {
	return m != nil && len(m.Modules[module]) > 0
}

// Entry returns the module entry for version, or nil.
func (m *Manifest) Entry(module, version string) *ModuleEntry {
	if m == nil {
		return nil
	}
	for i := range m.Modules[module] {
		if m.Modules[module][i].Version == version {
			return &m.Modules[module][i]
		}
	}
	return nil
}
