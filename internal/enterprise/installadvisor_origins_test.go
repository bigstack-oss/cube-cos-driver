package enterprise

import (
	"os/exec"
	"strings"
	"testing"
)

// runOriginWait runs the script's console-origin wait with curl answering
// only for the addresses in up, and returns its output.
func runOriginWait(t *testing.T, wired, up string) string {
	t.Helper()
	s := installAdvisorScript
	a := strings.Index(s, "# --- wait for the web-console origins ---")
	b := strings.Index(s, "# The node half of the console:")
	if a < 0 || b < a {
		t.Fatal("cannot locate the console-origin wait in install-advisor.sh")
	}
	script := `set -uo pipefail
FRAMEWORK=appfw; NS=cube-advisor; WIRED=(` + wired + `)
CONSOLE_WAIT_TRIES=7; CONSOLE_WAIT_SLEEP=0
reaps=0; reap_error_lbs() { reaps=$((reaps+1)); echo "REAP"; }
source() { :; }
curl() { for u in ` + up + `; do [ "${@: -1}" = "https://$u/" ] && { printf 302; return 0; }; done; printf 000; return 7; }
openstack() { printf 'kube_service_appfw_cube-advisor_cube-advisor-web-10-0-0-11 ERROR\nother ACTIVE\n'; }
` + s[a:b] + `
echo "REAPS=$reaps"
`
	o, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("bash: %v\n%s", err, o)
	}
	return string(o)
}

func TestConsoleOriginWaitPassesWhenEveryOriginAnswers(t *testing.T) {
	out := runOriginWait(t, "10.0.0.10 10.0.0.11", "10.0.0.10 10.0.0.11")
	if !strings.Contains(out, "web console origins answering: 10.0.0.10 10.0.0.11.") {
		t.Errorf("no success line:\n%s", out)
	}
	if strings.Contains(out, "warning:") || !strings.Contains(out, "REAPS=0") {
		t.Errorf("warned or reaped with every origin up:\n%s", out)
	}
}

// An origin whose LB never comes up is named with its LB state, errored LBs
// are reaped while waiting, and the install is not failed for it.
func TestConsoleOriginWaitNamesAnOriginThatNeverAnswers(t *testing.T) {
	out := runOriginWait(t, "10.0.0.10 10.0.0.11", "10.0.0.10")
	if !strings.Contains(out, "warning: web console origin 10.0.0.11 never answered (load balancer: ERROR)") {
		t.Errorf("the dead origin is not named with its LB state:\n%s", out)
	}
	if strings.Contains(out, "origin 10.0.0.10 never answered") {
		t.Errorf("an answering origin was named:\n%s", out)
	}
	if !strings.Contains(out, "REAPS=1") {
		t.Errorf("errored LBs were not reaped while waiting:\n%s", out)
	}
}

func TestConsoleOriginWaitSkipsWithNoConsole(t *testing.T) {
	out := runOriginWait(t, "", "")
	if strings.Contains(out, "waiting for the web console origins") || strings.Contains(out, "warning:") {
		t.Errorf("waited with no console origins:\n%s", out)
	}
}
