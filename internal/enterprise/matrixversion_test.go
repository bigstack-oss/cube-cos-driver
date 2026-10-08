package enterprise

import "testing"

func TestArtifactVersion(t *testing.T) {
	cases := []struct{ module, file, want string }{
		{ModuleCMP, "cube-portal-2.1.1+rev4902.pigz", "2.1.1"},
		{ModuleCMP, "cube-portal-2.1.1.pigz", "2.1.1"},
		{ModuleAdvisor, "cube-advisor-0.4.25.pigz", "0.4.25"},
		{ModuleAdvisor, "cube-advisor-0.4.25-g1a2b3c4.pigz", "0.4.25"},
		{ModuleAdvisor, "cube-advisor-0.4.25+build.7.pigz", "0.4.25"},
	}
	for _, c := range cases {
		got, err := ArtifactVersion(c.module, c.file)
		if err != nil || got != c.want {
			t.Errorf("ArtifactVersion(%q,%q) = %q,%v want %q", c.module, c.file, got, err, c.want)
		}
	}
}

func TestArtifactVersion_Unparseable(t *testing.T) {
	for _, f := range []string{"cube-portal-latest.pigz", "portal.pigz", "cube-advisor-0.4.pigz", ""} {
		if v, err := ArtifactVersion(ModuleCMP, f); err == nil {
			t.Errorf("ArtifactVersion(cmp,%q) = %q, want error", f, v)
		}
	}
	if _, err := ArtifactVersion(ModuleCMP, "cube-advisor-0.4.25.pigz"); err == nil {
		t.Error("an advisor file must not parse as a cmp artifact")
	}
}

func TestManifest_Entry(t *testing.T) {
	m := &Manifest{Modules: map[string][]ModuleEntry{
		ModuleCMP: {{Version: "2.1.1", Status: "supported"}, {Version: "2.1.0", Status: "deprecated", Reason: "old"}},
	}}
	if e := m.Entry(ModuleCMP, "2.1.0"); e == nil || e.Status != "deprecated" {
		t.Fatalf("Entry 2.1.0 = %+v", e)
	}
	if m.Entry(ModuleCMP, "9.9.9") != nil || m.Entry(ModuleAdvisor, "0.4.25") != nil {
		t.Fatal("unlisted version/module must be nil")
	}
	if !m.Constrains(ModuleCMP) || m.Constrains(ModuleAdvisor) {
		t.Fatal("Constrains wrong")
	}
	var nilM *Manifest
	if nilM.Constrains(ModuleCMP) || nilM.Entry(ModuleCMP, "2.1.1") != nil {
		t.Fatal("nil manifest must constrain nothing")
	}
}
