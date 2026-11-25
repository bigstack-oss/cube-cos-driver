package enterprise

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The scripts the driver pushes, by name, for the assertions below.
var rancherScripts = map[string]*string{
	"install-advisor.sh":   &installAdvisorScript,
	"uninstall-advisor.sh": &uninstallAdvisorScript,
	"install-portal.sh":    &installPortalScript,
	"uninstall-portal.sh":  &uninstallPortalScript,
}

// Every script uses the shared login and none keeps the old one.
func TestScriptsUseSharedRancherLogin(t *testing.T) {
	for name, s := range rancherScripts {
		if strings.Contains(*s, strings.TrimSpace(rancherKubeconfigMarker)) {
			t.Errorf("%s: marker not replaced", name)
		}
		if !strings.Contains(*s, rancherKubeconfigSnippet) {
			t.Errorf("%s: does not carry the shared rancher login", name)
		}
		for _, bad := range []string{
			"yes 1 |",
			"login --skip-verify --token",
			`cluster kf "$FRAMEWORK" 2>/dev/null`,
		} {
			if strings.Contains(*s, bad) {
				t.Errorf("%s: still has the old login (%q)", name, bad)
			}
		}
		if i, j := strings.Index(*s, rancherKubeconfigSnippet), strings.Index(*s, `KC="/tmp/`); j < 0 || j > i {
			t.Errorf("%s: KC must be set before the rancher login", name)
		}
	}
}

// runRancherLogin runs the snippet with sudo, rancher, curl and kubectl
// stubbed. ca says whether the node CA exists; help is the CLI's login help;
// env adds stub knobs. It returns the output and the rancher calls made.
func runRancherLogin(t *testing.T, ca bool, help string, env ...string) (string, []string, error) {
	t.Helper()
	dir := t.TempDir()
	caPath := filepath.Join(dir, "server.pem")
	if ca {
		if err := os.WriteFile(caPath, []byte("pem"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	calls := filepath.Join(dir, "calls")
	if strings.Count(rancherKubeconfigSnippet, `sudo "$RANCHER" `) != 2 {
		t.Fatal("cannot stub the rancher CLI in rancher-kubeconfig.sh")
	}
	script := `set -uo pipefail
fail() { echo "ERROR: $*" >&2; exit 1; }
FRAMEWORK=appfw; CTRL=10.0.0.250; RT=tok; KC="$STUB_DIR/kc"
sudo() { "$@"; }
rancher_stub() {
  printf '%s\n' "$*" >>"$STUB_DIR/calls"
  local a=("$@")
  [ "${a[0]}" = --config ] && { [ -d "${a[1]}" ] || { echo "no cfg dir" >&2; return 1; }; a=("${a[@]:2}"); }
  case "${a[0]} ${a[1]:-}" in
    "login --help") printf '%s\n' "$STUB_HELP" ;;
    "login "*) [ -z "${STUB_LOGIN_FAIL:-}" ] || { echo "Bad response statusCode [401]" >&2; return 1; } ;;
    "cluster kf") [ -n "${STUB_KF_EMPTY:-}" ] || echo "server: https://x/k8s/clusters/${a[2]}" ;;
  esac
}
curl() {
  local url="${*: -1}"
  printf 'curl %s\n' "$*" >>"$STUB_DIR/calls"
  case "$url" in
    *"/v3/clusters?name=appfw") [ -n "${STUB_NO_CLUSTER:-}" ] && echo '{"data":[]}' || echo '{"data":[{"id":"c-m-1"}]}' ;;
    *"/v3/projects?clusterId=c-m-1&name=Default") echo '{"data":[{"id":"c-m-1:p-1"}]}' ;;
    *) echo '{"data":[]}' ;;
  esac
}
kubectl() { sed -n 's/^server: //p' "$KC"; }
` + strings.ReplaceAll(rancherKubeconfigSnippet, `"$RANCHER" `, `rancher_stub `) + `
echo "SRV=$SRV CTX=$RANCHER_CTX"
[ -e "$RANCHER_CFG" ] && echo "LEFTOVER_CFG"
true`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "STUB_DIR="+dir, "STUB_HELP="+help, "RANCHER_CA="+caPath)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	b, _ := os.ReadFile(calls)
	return string(out), strings.Split(strings.TrimSpace(string(b)), "\n"), err
}

func findCall(calls []string, sub string) string {
	for _, c := range calls {
		if strings.Contains(c, sub) {
			return c
		}
	}
	return ""
}

const helpOld = "   --skip-verify  Skip verification of the CACerts presented by the Server\n   --cacert value"
const helpNew = "   --context value\n   --token value, -t value\n   --cacert value\n   --name value"

func TestRancherLoginPrefersNodeCA(t *testing.T) {
	out, calls, err := runRancherLogin(t, true, helpOld)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	login := findCall(calls, " login --cacert ")
	if login == "" || strings.Contains(login, "--skip-verify") {
		t.Fatalf("login does not use the node CA: %q", calls)
	}
	if !strings.HasPrefix(login, "--config ") || strings.Contains(login, ".rancher") {
		t.Errorf("login must use a private config dir: %q", login)
	}
	if !strings.Contains(login, "--context c-m-1:p-1 https://10.0.0.250:10443") {
		t.Errorf("login does not select the framework's context: %q", login)
	}
	if findCall(calls, "cluster kf c-m-1") == "" {
		t.Errorf("kf must target the framework's cluster id: %q", calls)
	}
	if !strings.Contains(out, "SRV=https://x/k8s/clusters/c-m-1 CTX=c-m-1:p-1") {
		t.Errorf("unexpected result:\n%s", out)
	}
	if strings.Contains(out, "LEFTOVER_CFG") {
		t.Error("the private rancher config dir (holds the token) was not removed")
	}
}

func TestRancherLoginFallsBackToSkipVerifyOnOldCLI(t *testing.T) {
	out, calls, err := runRancherLogin(t, false, helpOld)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if findCall(calls, " login --skip-verify --token tok --context c-m-1:p-1 ") == "" {
		t.Errorf("old CLI without a CA must log in with --skip-verify: %q", calls)
	}
	if findCall(calls, "curl -sSf -k ") == "" {
		t.Errorf("API lookups must match the login's TLS mode: %q", calls)
	}
}

func TestRancherLoginFailsWithoutCAOnNewCLI(t *testing.T) {
	out, calls, err := runRancherLogin(t, false, helpNew)
	if err == nil || !strings.Contains(out, "no --skip-verify") {
		t.Fatalf("want a loud failure, got err=%v\n%s", err, out)
	}
	if findCall(calls, " login --token") != "" || findCall(calls, "--skip-verify --token") != "" {
		t.Errorf("must not attempt a login: %q", calls)
	}
}

func TestRancherLoginFailsWhenFrameworkClusterMissing(t *testing.T) {
	out, calls, err := runRancherLogin(t, true, helpNew, "STUB_NO_CLUSTER=1")
	if err == nil || !strings.Contains(out, "rancher cluster 'appfw' not found") {
		t.Fatalf("want a not-found failure, got err=%v\n%s", err, out)
	}
	if findCall(calls, " login ") != "" {
		t.Errorf("must not log in (or fall back to a stored context): %q", calls)
	}
}

func TestRancherLoginFailureIsLoud(t *testing.T) {
	out, calls, err := runRancherLogin(t, true, helpNew, "STUB_LOGIN_FAIL=1")
	if err == nil || !strings.Contains(out, "context c-m-1:p-1) failed: Bad response statusCode [401]") {
		t.Fatalf("want the login error surfaced, got err=%v\n%s", err, out)
	}
	if findCall(calls, "cluster kf") != "" {
		t.Errorf("must not run kf after a failed login: %q", calls)
	}
}

func TestEmptyKubeconfigNamesClusterAndContext(t *testing.T) {
	out, _, err := runRancherLogin(t, true, helpNew, "STUB_KF_EMPTY=1")
	if err == nil || !strings.Contains(out, "empty framework kubeconfig for cluster 'appfw' (c-m-1, context c-m-1:p-1)") {
		t.Fatalf("want cluster + context in the error, got err=%v\n%s", err, out)
	}
}
