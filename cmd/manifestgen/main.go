// manifestgen merges matrix/matrix.yaml with matrix/facts into the embedded manifests.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/bigstack-oss/cube-cos-driver/internal/enterprise"
	"gopkg.in/yaml.v3"
)

type facts struct {
	Version           string            `json:"version"`
	AirgapSupported   *bool             `json:"airgapSupported"`
	Import            json.RawMessage   `json:"import"`
	Appfw             *enterprise.Appfw `json:"appfw"`
	AdvisorReleaseKey *struct {
		P384    string `json:"p384"`
		MLDSA87 string `json:"mldsa87"`
	} `json:"advisorReleaseKey"`
}

var (
	plainVersion = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	statuses     = map[string]bool{"supported": true, "untested": true, "deprecated": true, "blocked": true}
)

func generate(matrixPath, factsDir, keysDir string) (map[string][]byte, error) {
	raw, err := os.ReadFile(matrixPath)
	if err != nil {
		return nil, err
	}
	var matrix struct {
		Intermediate []string                                       `yaml:"intermediate"`
		Releases     map[string]map[string][]enterprise.ModuleEntry `yaml:"releases"`
	}
	if err := yaml.Unmarshal(raw, &matrix); err != nil {
		return nil, fmt.Errorf("%s: %w", matrixPath, err)
	}
	for _, v := range matrix.Intermediate {
		if _, ok := matrix.Releases[v]; ok {
			return nil, fmt.Errorf("%s is intermediate (upgrade hop only) and must not have module rows", v)
		}
	}
	out := map[string][]byte{}
	for ver, modules := range matrix.Releases {
		fb, err := os.ReadFile(filepath.Join(factsDir, "cos-"+ver+".json"))
		if err != nil {
			return nil, fmt.Errorf("release %s: no facts (%w)", ver, err)
		}
		var f facts
		if err := json.Unmarshal(fb, &f); err != nil {
			return nil, fmt.Errorf("cos-%s.json: %w", ver, err)
		}
		m := enterprise.Manifest{Schema: 2, Name: "v" + ver, AirgapSupported: f.AirgapSupported, Appfw: f.Appfw, Modules: modules}
		m.Match.Version = ver
		if len(f.Import) > 0 {
			if err := json.Unmarshal(f.Import, &m.Import); err != nil {
				return nil, err
			}
		}
		if k := f.AdvisorReleaseKey; k != nil {
			p, err := os.ReadFile(filepath.Join(keysDir, k.P384))
			if err != nil {
				return nil, err
			}
			ml, err := os.ReadFile(filepath.Join(keysDir, k.MLDSA87))
			if err != nil {
				return nil, err
			}
			m.Trust = &enterprise.Trust{AdvisorReleaseKey: &enterprise.TrustKey{P384: string(p), MLDSA87: string(ml)}}
		}
		for mod, entries := range modules {
			seen := map[string]bool{}
			for _, e := range entries {
				switch {
				case !plainVersion.MatchString(e.Version):
					return nil, fmt.Errorf("%s %s: version %q must be MAJOR.MINOR.PATCH (no build tag)", ver, mod, e.Version)
				case !statuses[e.Status]:
					return nil, fmt.Errorf("%s %s %s: unknown status %q", ver, mod, e.Version, e.Status)
				case (e.Status == "blocked" || e.Status == "deprecated") && e.Reason == "":
					return nil, fmt.Errorf("%s %s %s: %s needs a reason", ver, mod, e.Version, e.Status)
				case seen[e.Version]:
					return nil, fmt.Errorf("%s %s: version %s listed twice", ver, mod, e.Version)
				}
				seen[e.Version] = true
			}
		}
		b, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return nil, err
		}
		out["v"+ver+".json"] = append(b, '\n')
	}
	return out, nil
}

func main() {
	matrix := flag.String("matrix", "matrix/matrix.yaml", "")
	factsDir := flag.String("facts", "matrix/facts", "")
	keysDir := flag.String("keys", "matrix/keys", "")
	outDir := flag.String("out", "internal/enterprise/manifests", "")
	flag.Parse()
	files, err := generate(*matrix, *factsDir, *keysDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "manifestgen:", err)
		os.Exit(1)
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(*outDir, n), files[n], 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "manifestgen:", err)
			os.Exit(1)
		}
	}
}
