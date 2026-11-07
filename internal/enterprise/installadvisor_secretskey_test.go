package enterprise

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// runAdvisorSecretsKey runs the script's secrets-key block with kubectl
// stubbed by kstub (a bash function body) and returns its output and the key
// it would pass to the chart.
func runAdvisorSecretsKey(t *testing.T, kstub string) (out, key string, err error) {
	t.Helper()
	s := installAdvisorScript
	a := strings.Index(s, "# The Advisor secrets key, which encrypts data it stores")
	b := strings.Index(s, "# A self-signed keypair for the LB address.")
	if a < 0 || b < a {
		t.Fatal("cannot locate the secrets-key block in install-advisor.sh")
	}
	script := `set -uo pipefail
NS=cube-advisor; TMPDIRS=()
fail() { echo "ERROR: $*" >&2; exit 1; }
kstub() { ` + kstub + `; }; K=kstub
` + s[a:b] + `
echo "KEY=$(cat "$SKEYDIR/secrets.key")"
rm -rf "${TMPDIRS[@]}"
`
	o, err := exec.Command("bash", "-c", script).CombinedOutput()
	out = string(o)
	if m := regexp.MustCompile(`(?m)^KEY=(.*)$`).FindStringSubmatch(out); m != nil {
		key = m[1]
	}
	return out, key, err
}

// A re-run must pass back the key already in the Secret, or data the
// Advisor encrypted with it becomes unreadable.
func TestAdvisorInstallCarriesTheSecretsKey(t *testing.T) {
	// base64 of "kept-key-0123456789abcdef"
	out, key, err := runAdvisorSecretsKey(t,
		`case "$*" in *jsonpath*) printf %s a2VwdC1rZXktMDEyMzQ1Njc4OWFiY2RlZg== ;; esac; return 0`)
	if err != nil {
		t.Fatalf("bash: %v\n%s", err, out)
	}
	if key != "kept-key-0123456789abcdef" {
		t.Errorf("key = %q, want the one in the Secret\n%s", key, out)
	}
	if !strings.Contains(out, "carrying the existing secrets key") {
		t.Errorf("log does not say the key was carried:\n%s", out)
	}
}

// A first install, or a release from a chart that predates the key, gets one
// minted locally: nothing is fetched.
func TestAdvisorInstallMintsTheSecretsKeyWhenAbsent(t *testing.T) {
	for name, stub := range map[string]string{
		"no secret":         `return 1`,
		"secret without it": `return 0`,
	} {
		out, key, err := runAdvisorSecretsKey(t, stub)
		if err != nil {
			t.Fatalf("%s: bash: %v\n%s", name, err, out)
		}
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(key) {
			t.Errorf("%s: key = %q, want 64 hex chars", name, key)
		}
		if !strings.Contains(out, "issued a secrets key") {
			t.Errorf("%s: log does not say a key was issued:\n%s", name, out)
		}
	}
}

// A Secret that exists but cannot be read must stop the install rather than
// mint a new key over the old one.
func TestAdvisorInstallRefusesToRotateAnUnreadableSecretsKey(t *testing.T) {
	out, key, err := runAdvisorSecretsKey(t, `case "$*" in *jsonpath*) return 1 ;; esac; return 0`)
	if err == nil || key != "" {
		t.Fatalf("install went on with key %q:\n%s", key, out)
	}
	if !strings.Contains(out, "refusing to upgrade and rotate it") {
		t.Errorf("unexpected failure:\n%s", out)
	}
}

func TestAdvisorInstallPassesTheSecretsKeyToTheChart(t *testing.T) {
	if !contains(installAdvisorScript, `--set-file secretsKey="$SKEYDIR/secrets.key"`) {
		t.Error("install-advisor.sh does not pass secretsKey to helm")
	}
}
