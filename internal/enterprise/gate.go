package enterprise

import "fmt"

func CheckModule(mf *Manifest, module, file string, lab bool) (string, error) {
	if (module != ModuleCMP && module != ModuleAdvisor) || !mf.Constrains(module) {
		return "", nil
	}
	ver, err := ArtifactVersion(module, file)
	if err != nil {
		return "", err
	}
	e := mf.Entry(module, ver)
	if e == nil {
		msg := fmt.Sprintf("%s %s is not in the %s support matrix", module, ver, mf.Name)
		if lab {
			return msg + " (lab install, continuing)", nil
		}
		return "", fmt.Errorf("%s", msg)
	}
	switch e.Status {
	case "blocked":
		return "", fmt.Errorf("%s %s is blocked on %s: %s %s", module, ver, mf.Name, e.Reason, e.Link)
	case "deprecated", "untested":
		w := fmt.Sprintf("%s %s is %s on %s", module, ver, e.Status, mf.Name)
		if e.Reason != "" {
			w += ": " + e.Reason
		}
		return w, nil
	}
	return "", nil
}
