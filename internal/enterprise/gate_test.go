package enterprise

import (
	"strings"
	"testing"
)

func TestCheckModule(t *testing.T) {
	mf := &Manifest{Name: "v3.2.0", Modules: map[string][]ModuleEntry{
		ModuleCMP: {
			{Version: "2.1.1", Status: "supported"},
			{Version: "2.0.0", Status: "blocked", Reason: "breaks keycloak", Link: "cubecmp#1"},
			{Version: "2.1.0", Status: "deprecated", Reason: "use 2.1.1"},
		},
		ModuleAdvisor: {{Version: "0.4.25", Status: "untested"}},
	}}
	type want struct{ warn, err string }
	cases := []struct {
		name, module, file string
		lab                bool
		want               want
	}{
		{"supported build tag ignored", ModuleCMP, "cube-portal-2.1.1+rev9999.pigz", false, want{}},
		{"unlisted", ModuleCMP, "cube-portal-2.2.0.pigz", false, want{err: "not in the v3.2.0 support matrix"}},
		{"unlisted lab", ModuleCMP, "cube-portal-2.2.0.pigz", true, want{warn: "not in the v3.2.0 support matrix"}},
		{"blocked", ModuleCMP, "cube-portal-2.0.0.pigz", false, want{err: "breaks keycloak"}},
		{"blocked lab", ModuleCMP, "cube-portal-2.0.0.pigz", true, want{err: "blocked"}},
		{"deprecated", ModuleCMP, "cube-portal-2.1.0.pigz", false, want{warn: "use 2.1.1"}},
		{"untested", ModuleAdvisor, "cube-advisor-0.4.25.pigz", false, want{warn: "untested"}},
		{"unparseable", ModuleCMP, "cube-portal-latest.pigz", true, want{err: "can't read"}},
		{"appfw unconstrained", ModuleAppFW, "", false, want{}},
	}
	for _, c := range cases {
		warn, err := CheckModule(mf, c.module, c.file, c.lab)
		if c.want.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.want.err) {
				t.Errorf("%s: err = %v, want containing %q", c.name, err, c.want.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected err %v", c.name, err)
		}
		if (c.want.warn == "") != (warn == "") || !strings.Contains(warn, c.want.warn) {
			t.Errorf("%s: warn = %q, want containing %q", c.name, warn, c.want.warn)
		}
	}
	if w, err := CheckModule(nil, ModuleCMP, "anything.pigz", false); w != "" || err != nil {
		t.Errorf("no manifest must not gate: %q %v", w, err)
	}
}

func TestCheckModule_UnknownStatusRefused(t *testing.T) {
	mf := &Manifest{Name: "v3.2.0", Modules: map[string][]ModuleEntry{
		ModuleCMP: {{Version: "2.1.1", Status: "suported"}},
	}}
	_, err := CheckModule(mf, ModuleCMP, "cube-portal-2.1.1.pigz", false)
	if err == nil || !strings.Contains(err.Error(), `unknown status "suported"`) {
		t.Fatalf("err = %v, want unknown status", err)
	}
}
