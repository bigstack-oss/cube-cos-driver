package enterprise

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The probe's python, run against a stubbed openstack CLI: two compute hosts,
// one with room for 3 m1.amphora (RAM-bound at 1.0 ratio), one full on disk.
func TestAmphoraCapacityProbeCountsPerHostFit(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "octavia.conf")
	os.WriteFile(conf, []byte("[controller_worker]\namp_flavor_id = 16443\nloadbalancer_topology = SINGLE\n"), 0o644)
	stub := `#!/bin/bash
case "$*" in
  "loadbalancer list -f json") echo '[{"name":"kube_service_appfw_cube-advisor_cube-advisor","provisioning_status":"ACTIVE"},{"name":"kube_service_appfw_kube-system_ingress-lb","provisioning_status":"ACTIVE"}]' ;;
  "flavor show 16443 -f json") echo '{"name":"m1.amphora","vcpus":1,"ram":1024,"disk":20}' ;;
  "resource provider list -f json") echo '[{"uuid":"h1"},{"uuid":"h2"},{"uuid":"pool"}]' ;;
  "resource provider inventory list h1 -f json") echo '[{"resource_class":"VCPU","total":16,"reserved":0,"allocation_ratio":4.0,"max_unit":16},{"resource_class":"MEMORY_MB","total":65536,"reserved":4096,"allocation_ratio":1.0,"max_unit":65536},{"resource_class":"DISK_GB","total":1000,"reserved":0,"allocation_ratio":1.0,"max_unit":1000}]' ;;
  "resource provider usage show h1 -f json") echo '[{"resource_class":"VCPU","usage":10},{"resource_class":"MEMORY_MB","usage":58368},{"resource_class":"DISK_GB","usage":100}]' ;;
  "resource provider inventory list h2 -f json") echo '[{"resource_class":"VCPU","total":16,"reserved":0,"allocation_ratio":4.0,"max_unit":16},{"resource_class":"MEMORY_MB","total":65536,"reserved":0,"allocation_ratio":1.0,"max_unit":65536},{"resource_class":"DISK_GB","total":100,"reserved":0,"allocation_ratio":1.0,"max_unit":100}]' ;;
  "resource provider usage show h2 -f json") echo '[{"resource_class":"DISK_GB","usage":90}]' ;;
  "resource provider inventory list pool -f json") echo '[{"resource_class":"DISK_GB","total":100,"reserved":0,"allocation_ratio":1.0,"max_unit":100}]' ;;
  *) exit 1 ;;
esac
`
	os.WriteFile(filepath.Join(dir, "openstack"), []byte(stub), 0o755)
	probe := strings.Replace(amphoraCapacityProbe, "/etc/octavia/octavia.conf", conf, 1)
	probe = strings.Replace(probe, "source /etc/admin-openrc.sh && ", "", 1)
	cmd := exec.Command("bash", "-c", strings.Replace(probe, "%s", shellQuote(advisorLBPrefix("appfw")), 1))
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe: %v\n%s", err, out)
	}
	c := parseAmphoraCapacity(strings.Split(strings.TrimSpace(string(out)), "\n"))
	// h1: RAM (61440-58368)/1024 = 3; h2: disk (100-90)/20 = 0; "pool" has no VCPU.
	if !c.known || c.fit != 3 || c.flavor != "m1.amphora" || c.ramMB != 1024 || c.diskG != 20 || c.standby {
		t.Fatalf("capacity = %+v\n%s", c, out)
	}
	if len(c.lbs) != 1 || c.lbs["kube_service_appfw_cube-advisor_cube-advisor"] != "ACTIVE" {
		t.Fatalf("lbs = %v, want only this release's", c.lbs)
	}
}

func TestAdvisorLBsToCreate(t *testing.T) {
	p := InstallParams{AdvisorPool: []string{"10.0.0.60", "10.0.0.61", "10.0.0.62"}}
	pre := advisorLBPrefix("appfw")
	got := advisorLBsToCreate("appfw", p, map[string]string{
		pre + "cube-advisor":               "ACTIVE",
		pre + "cube-advisor-web-10-0-0-60": "ACTIVE",
		pre + "cube-advisor-web-10-0-0-61": "ERROR",
	})
	if strings.Join(got, ",") != pre+"cube-advisor-web-10-0-0-61" {
		t.Errorf("got %v: want only the origin in ERROR (the third address is not wired)", got)
	}
	if got := advisorLBsToCreate("appfw", InstallParams{AdvisorPool: []string{"10.0.0.60"}}, nil); len(got) != 1 {
		t.Errorf("one address leaves the console off, so only the advisor LB: %v", got)
	}
}

func capacityScript(lines ...string) func(string) ([]string, error) {
	return frameworkActiveAfterCreate("appfw", func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "amp_flavor_id") {
			return lines, nil
		}
		if strings.Contains(cmd, "addr=") {
			return []string{"free"}, nil
		}
		return nil, nil
	})
}

func advisorInstall() InstallParams {
	return InstallParams{Project: "appfw", Framework: "appfw", LBIP: "10.32.36.120", OSImage: "r.raw",
		AdvisorFile: "cube-advisor-1.2.3.pigz", AdvisorLBIP: "10.0.0.9",
		AdvisorPool: []string{"10.0.0.10", "10.0.0.11"}}
}

// The 1cc r630 case: three LBs to create, room for two amphorae. Preflight
// refuses and states the cost, rather than leaving an origin LB in ERROR.
func TestPreflightRefusesWhenAmphoraeDoNotFit(t *testing.T) {
	m, mc := newTestMgr(t, capacityScript("topology SINGLE", "flavor m1.amphora 1 1024 20", "fit 2"))
	m.Start("cl1", "advisor", "10.32.10.140", "pw", advisorInstall(), false, false, nil)
	waitState(t, m, "cl1", "advisor", "error")
	in, _ := m.Status("cl1", "advisor")
	for _, want := range []string{"3 new load balancer(s)", "m1.amphora (1 vCPU, 1024 MB RAM, 20 GB disk) = 3 amphorae", "fits 2"} {
		if !strings.Contains(in.Steps[0].Err, want) {
			t.Errorf("preflight Err = %q, want %q", in.Steps[0].Err, want)
		}
	}
	if containsCmd(mc.Runs, advisorInstallScriptName) {
		t.Error("the install ran after preflight refused")
	}
}

// ACTIVE_STANDBY doubles the amphorae per load balancer.
func TestPreflightCountsTwoAmphoraePerLBUnderActiveStandby(t *testing.T) {
	m, _ := newTestMgr(t, capacityScript("topology ACTIVE_STANDBY", "flavor m1.amphora 1 1024 20", "fit 5"))
	m.Start("cl1", "advisor", "10.32.10.140", "pw", advisorInstall(), false, false, nil)
	waitState(t, m, "cl1", "advisor", "error")
	in, _ := m.Status("cl1", "advisor")
	if !strings.Contains(in.Steps[0].Err, "= 6 amphorae") {
		t.Errorf("preflight Err = %q, want 6 amphorae", in.Steps[0].Err)
	}
}

func TestPreflightPassesWhenAmphoraeFit(t *testing.T) {
	m, _ := newTestMgr(t, capacityScript("topology SINGLE", "flavor m1.amphora 1 1024 20", "fit 3"))
	m.Start("cl1", "advisor", "10.32.10.140", "pw", advisorInstall(), false, false, nil)
	waitState(t, m, "cl1", "advisor", "done")
}

// An upgrade whose load balancers are all up needs no new amphorae.
func TestPreflightNeedsNoAmphoraeWhenEveryLBIsUp(t *testing.T) {
	pre := advisorLBPrefix("appfw")
	m, _ := newTestMgr(t, capacityScript("topology SINGLE", "flavor m1.amphora 1 1024 20", "fit 0",
		"lb "+pre+"cube-advisor ACTIVE", "lb "+pre+"cube-advisor-web-10-0-0-10 ACTIVE", "lb "+pre+"cube-advisor-web-10-0-0-11 ACTIVE"))
	m.Start("cl1", "advisor", "10.32.10.140", "pw", advisorInstall(), false, false, nil)
	waitState(t, m, "cl1", "advisor", "done")
}

// runOriginCheck runs the script's origin-LB block with openstack stubbed to
// report status for each origin LB.
func runOriginCheck(t *testing.T, statuses map[string]string) (string, error) {
	t.Helper()
	s := installAdvisorScript
	a := strings.Index(s, "# --- every console origin got its load balancer ---")
	b := strings.Index(s, `echo "cube-advisor installed and verified`)
	if a < 0 || b < a {
		t.Fatal("cannot locate the origin check in install-advisor.sh")
	}
	stub := "openstack() { case \"$3\" in\n"
	for n, st := range statuses {
		stub += "  " + n + ") echo " + st + " ;;\n"
	}
	stub += "esac; }\n"
	script := `set -uo pipefail
FRAMEWORK=appfw; NS=cube-advisor; ADVISOR_LB_IP=10.0.0.9; WIRED=(10.0.0.10 10.0.0.11)
fail() { echo "ERROR: $*"; exit 1; }
sleep() { :; }; reap_error_lbs() { :; }
` + stub + s[a:b]
	o, err := exec.Command("bash", "-c", script).CombinedOutput()
	return string(o), err
}

func TestInstallFailsNamingAnOriginWithoutALoadBalancer(t *testing.T) {
	out, err := runOriginCheck(t, map[string]string{
		"kube_service_appfw_cube-advisor_cube-advisor-web-10-0-0-10": "ACTIVE",
		"kube_service_appfw_cube-advisor_cube-advisor-web-10-0-0-11": "ERROR",
	})
	if err == nil || !strings.Contains(out, "web-console origin(s) without a load balancer: 10.0.0.11 (ERROR)") {
		t.Fatalf("err=%v, out:\n%s", err, out)
	}
	if strings.Contains(out, "10.0.0.10 (") {
		t.Errorf("the ACTIVE origin is reported as missing:\n%s", out)
	}
}

func TestInstallPassesWhenEveryOriginIsActive(t *testing.T) {
	out, err := runOriginCheck(t, map[string]string{
		"kube_service_appfw_cube-advisor_cube-advisor-web-10-0-0-10": "ACTIVE",
		"kube_service_appfw_cube-advisor_cube-advisor-web-10-0-0-11": "ACTIVE",
	})
	if err != nil || !strings.Contains(out, "web-console origins: 2 load balancer(s) ACTIVE.") {
		t.Fatalf("err=%v, out:\n%s", err, out)
	}
}
