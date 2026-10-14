package enterprise

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// runAdvisorOrigins runs the script's SAN block and web-console origins block
// with kubectl stubbed to report ingress, and returns their output and SANs.
func runAdvisorOrigins(t *testing.T, pool, ingress string) (out, sans string, addrs []string) {
	t.Helper()
	s := installAdvisorScript
	a := strings.Index(s, `SANS="IP:${ADVISOR_LB_IP}"`)
	b := strings.Index(s, `TLSDIR="$(mktemp -d)"`)
	c := strings.Index(s, "WC_ARGS=()\n")
	d := strings.Index(s, "# --- the inference provider ---")
	if a < 0 || b < a || c < 0 || d < c {
		t.Fatal("cannot locate the SAN and origins blocks in install-advisor.sh")
	}
	script := `set -uo pipefail
ADVISOR_LB_IP=10.0.0.9; CONSOLE_POOL="` + pool + `"; FRAMEWORK=appfw; CTRL=10.0.0.250
kstub() { printf %s "` + ingress + `"; }; K=kstub
` + s[a:b] + s[c:d] + `
echo "SANS=$SANS"
printf 'ARG=%s\n' "${WC_ARGS[@]}"
`
	o, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("bash: %v\n%s", err, o)
	}
	out = string(o)
	if m := regexp.MustCompile(`(?m)^SANS=(.*)$`).FindStringSubmatch(out); m != nil {
		sans = m[1]
	}
	for _, m := range regexp.MustCompile(`(?m)^ARG=webConsole\.origins\[\d+\]\.address=(.*)$`).FindAllStringSubmatch(out, -1) {
		addrs = append(addrs, m[1])
	}
	return out, sans, addrs
}

// A pool address with no origin behind it must stay out of the certificate,
// or it is in the SAN, unreachable, and offered as free by the next install.
func TestAdvisorCertificateCoversOnlyWiredConsoleAddresses(t *testing.T) {
	out, sans, addrs := runAdvisorOrigins(t, "10.0.0.10,10.0.0.11,10.0.0.12", "10.0.0.100")
	if sans != "IP:10.0.0.9,IP:10.0.0.10,IP:10.0.0.11" {
		t.Errorf("SANS = %q, want the LB IP and the two wired origins", sans)
	}
	if strings.Join(addrs, ",") != "10.0.0.10,10.0.0.11" {
		t.Errorf("origin addresses = %v", addrs)
	}
	if !strings.Contains(out, "web console enabled on 2 origin address(es): 10.0.0.10 10.0.0.11.") {
		t.Errorf("log does not count the configured origins:\n%s", out)
	}
	if !strings.Contains(out, "10.0.0.12 have no origin") {
		t.Errorf("the unused address is not reported:\n%s", out)
	}
}

func TestAdvisorCertificateWithoutIngressCoversTheCubeCOSOrigin(t *testing.T) {
	out, sans, addrs := runAdvisorOrigins(t, "10.0.0.10,10.0.0.11", "")
	if sans != "IP:10.0.0.9,IP:10.0.0.10" || strings.Join(addrs, ",") != "10.0.0.10" {
		t.Errorf("SANS = %q, origins = %v", sans, addrs)
	}
	if !strings.Contains(out, "web console enabled on 1 origin address(es): 10.0.0.10.") {
		t.Errorf("log does not count the configured origins:\n%s", out)
	}
}

func TestAdvisorCertificateWithoutConsole(t *testing.T) {
	_, sans, addrs := runAdvisorOrigins(t, "10.0.0.10", "10.0.0.100")
	if sans != "IP:10.0.0.9" || len(addrs) != 0 {
		t.Errorf("SANS = %q, origins = %v", sans, addrs)
	}
}
