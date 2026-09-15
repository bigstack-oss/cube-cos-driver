#!/bin/bash
# Deploy Cube AI Advisor into an app-framework and verify it end-to-end.
# advisor_register only pushes the chart + prereqs to Harbor; this installs
# the chart and confirms the advisor serves. Idempotent: safe to re-run.
#
# args: <framework> <advisor_lb_ip> <chart_version> [base_url]
set -uo pipefail
FRAMEWORK="${1:?framework name required}"
ADVISOR_LB_IP="${2:?advisor lb ip required}"
CHART_VER="${3:?chart version required}"
# The origin a browser reaches the advisor on; the chart builds its OAuth
# redirect_uri from it. Defaults to https on the LB: the session cookie is
# __Host- prefixed and Secure, so advisor-api refuses a plain-http origin. This
# script issues a self-signed certificate for that address below, so no external
# TLS terminator is needed and nothing is fetched.
#
# An operator with their own terminator or certificate passes the origin as $4
# and supplies the keypair through the chart directly.
BASE_URL="${4:-https://$ADVISOR_LB_IP}"
NS=cube-advisor

fail() { echo "ERROR: $*" >&2; exit 1; }

# One cleanup for one EXIT trap. Each `trap … EXIT` replaces the last, so the
# separate traps this script used to set meant only the final one ran and the
# /etc/hosts pin leaked — exactly what its own comment says must not happen.
HOSTS_PIN=""; TMPDIRS=()
cleanup() {
  [ -n "$HOSTS_PIN" ] && sudo sed -i "/${HOSTS_PIN}/d" /etc/hosts
  [ ${#TMPDIRS[@]} -gt 0 ] && rm -rf "${TMPDIRS[@]}"
  return 0
}
trap cleanup EXIT

# --- framework kubeconfig (rancher-proxied, rewritten to the control VIP) ---
CTRL="$(grep 'cubesys.control.vip' /etc/settings.txt | cut -d= -f2 | tr -d ' ')"
if [ -z "$CTRL" ]; then
  MGMT="$(grep 'cubesys.management' /etc/settings.txt | cut -d= -f2 | tr -d ' ')"
  CTRL="$(grep "net.if.addr.${MGMT}" /etc/settings.txt | cut -d= -f2 | tr -d ' ')"
fi
# terraform-cube.sh may print "Upgrading modules…" to stdout before the JSON on
# a fresh cluster; strip everything before the first { so jq sees clean JSON.
RT="$(terraform-cube.sh state pull 2>/dev/null | sed -n '/^{/,$p' | jq -r '.resources[]|select(.type=="rancher2_bootstrap").instances[0].attributes.token')"
[ -n "$RT" ] || fail "could not read rancher token"
# --skip-verify suppresses the trust prompt, so once >1 project exists (the
# framework adds its own) login lands on an interactive "Select a Project" prompt.
# Feed "1" (always a valid choice) so login persists non-interactively; the picked
# project is irrelevant since `cluster kf` takes the cluster name explicitly.
yes 1 | sudo /usr/local/bin/rancher login --skip-verify --token "$RT" "https://${CTRL}:10443" >/dev/null 2>&1
KC="/tmp/${FRAMEWORK}-advisor.kc"
sudo /usr/local/bin/rancher cluster kf "$FRAMEWORK" 2>/dev/null > "$KC"
SRV="$(kubectl config view --kubeconfig="$KC" -o jsonpath='{.clusters[0].cluster.server}')"
[ -n "$SRV" ] || fail "empty framework kubeconfig (rancher login/kf failed)"
kubectl config set-cluster "$(kubectl config view --kubeconfig="$KC" -o jsonpath='{.clusters[0].name}')" \
  --server="$(echo "$SRV" | sed "s#https://[^/]*#https://${CTRL}:10443#")" --kubeconfig="$KC" >/dev/null
K="kubectl --insecure-skip-tls-verify --kubeconfig=$KC"

# --- clear leftovers from a failed prior run ---
# A namespace still Terminating (its Service finalizer waiting on LB teardown)
# makes helm --create-namespace fail; and an Octavia LB stuck in provisioning
# ERROR can never be deleted or reused by the cloud-provider. Reap the errored
# LB first (scoped to this service's exact LB name), then wait the namespace out.
source /etc/admin-openrc.sh 2>/dev/null || true
LBNAME="kube_service_${FRAMEWORK}_${NS}_cube-advisor"
LBID_STATUS="$(openstack loadbalancer list -f value -c id -c name -c provisioning_status 2>/dev/null | awk -v n="$LBNAME" '$2 == n {print $1" "$3}')"
if [ -n "$LBID_STATUS" ] && [ "${LBID_STATUS##* }" = "ERROR" ]; then
  echo "reaping errored advisor LB ${LBID_STATUS%% *}…"
  openstack loadbalancer delete "${LBID_STATUS%% *}" --cascade 2>/dev/null || true
fi
if $K get namespace "$NS" -o jsonpath='{.status.phase}' 2>/dev/null | grep -q Terminating; then
  echo "waiting for namespace $NS to finish terminating…"
  for _ in $(seq 1 36); do
    $K get namespace "$NS" >/dev/null 2>&1 || break
    sleep 10
  done
  $K get namespace "$NS" >/dev/null 2>&1 && fail "namespace $NS still terminating — clear it before reinstalling"
fi

# --- install or upgrade the chart ---
# No "already installed, skipping" guard: helm upgrade --install is exactly the
# command for "may or may not exist", and skipping it made a re-run a no-op, so
# a new chart version could never reach a deployed framework. install-portal.sh
# keeps its guard because it runs plain `helm install`, which genuinely cannot
# re-run; the difference is the helm verb, not the intent.
#
# Re-running must not disturb what a prior install created: the secrets below
# are read back rather than regenerated, and so are the web keypair and the
# enrollment material — the latter is not a chart value the driver sets, so
# without reading it back an upgrade would render the release without that
# Secret and helm would delete it, taking every enrolled agent's trust with it.
RURL="$($K -n harbor get secret registry-details -o jsonpath='{.data.registryUrl}' 2>/dev/null | base64 -d)"
RPROJ="$($K -n harbor get secret registry-details -o jsonpath='{.data.registryExtensionProject}' 2>/dev/null | base64 -d)"
RUSER="$($K -n harbor get secret registry-details -o jsonpath='{.data.registryServiceAccount}' 2>/dev/null | base64 -d)"
RPASS="$($K -n harbor get secret registry-details -o jsonpath='{.data.registryServicePassword}' 2>/dev/null | base64 -d)"
[ -n "$RURL" ] || fail "registry-details.registryUrl is empty — appctl's registry setup did not complete"
echo "installing cube-advisor $CHART_VER from oci://$RURL/$RPROJ …"
# The registry hostname lives on the framework's ingress LB and only the
# node-local resolver knows it — dig it there (as import.sh does) and pin it
# in /etc/hosts for the duration of the helm pulls.
LOCAL_IP="$(hostname -I | awk '{print $1}')"
REG_IP="$(dig @"${LOCAL_IP}" "$RURL" A +short | tail -1)"
[ -n "$REG_IP" ] || fail "could not resolve registry $RURL via local resolver ${LOCAL_IP}"
# Pin it only for the duration of this run and undo even on failure — a
# leftover entry would mask a wrong/stale IP on the next run.
if ! grep -q "$RURL" /etc/hosts; then
  echo "$REG_IP $RURL" | sudo tee -a /etc/hosts >/dev/null
  HOSTS_PIN="$RURL"
fi
helm registry login "$RURL" -u "$RUSER" -p "$RPASS" --insecure >/dev/null 2>&1
# advisor_register can exit 0 having pushed nothing if the framework registry
# wasn't set up — fail clearly here, not on a cryptic helm pull.
helm show chart "oci://$RURL/$RPROJ/cube-advisor" --version "$CHART_VER" --insecure-skip-tls-verify >/dev/null 2>&1 \
  || fail "cube-advisor chart not in registry (oci://$RURL/$RPROJ/cube-advisor:$CHART_VER) — advisor_register pushed nothing; check the framework registry setup"

# Secrets: generate once, reuse on re-run (a re-run must not rotate creds a
# prior install already wrote into the running database).
if $K -n "$NS" get secret cube-advisor-secrets >/dev/null 2>&1; then
  DBPW=$($K -n "$NS" get secret cube-advisor-secrets -o jsonpath='{.data.dbPassword}' | base64 -d)
  APPPW=$($K -n "$NS" get secret cube-advisor-secrets -o jsonpath='{.data.appDbPassword}' | base64 -d)
else
  DBPW=$(openssl rand -hex 16); APPPW=$(openssl rand -hex 16)
fi

# A self-signed keypair for the LB address. SAN carries the IP: browsers
# reject a certificate that only has a CN. Reused on re-run for the same
# reason the passwords are: rotating it would make every browser that has
# accepted this advisor challenge it again after a routine upgrade.
TLSDIR="$(mktemp -d)"; TMPDIRS+=("$TLSDIR")
if $K -n "$NS" get secret cube-advisor-web-tls >/dev/null 2>&1; then
  $K -n "$NS" get secret cube-advisor-web-tls -o jsonpath='{.data.tls\.crt}' | base64 -d > "$TLSDIR/tls.crt"
  $K -n "$NS" get secret cube-advisor-web-tls -o jsonpath='{.data.tls\.key}' | base64 -d > "$TLSDIR/tls.key"
fi
if [ ! -s "$TLSDIR/tls.crt" ] || [ ! -s "$TLSDIR/tls.key" ]; then
  openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
    -subj "/CN=${ADVISOR_LB_IP}" -addext "subjectAltName=IP:${ADVISOR_LB_IP}" \
    -keyout "$TLSDIR/tls.key" -out "$TLSDIR/tls.crt" 2>/dev/null \
    || fail "could not issue the advisor's TLS certificate"
fi

# The enrollment CA and the tunnel listener's keypair. They live in the release,
# so an upgrade that does not pass them back renders without that Secret and
# helm deletes it — and every enrolled agent pins this CA and cannot be
# re-enrolled remotely, so losing it strands the whole fleet. Carried through
# when present, generated when not.
#
# Generated rather than left to an operator because the advisor registers its
# enrollment endpoint only when both CA halves are present: without them the
# API logs "enrollment disabled (need -dsn, -ca-cert and -ca-key)", /api/v1/enroll
# and /api/v1/releases both 404, and an advisor that installs cleanly cannot
# enrol a single cluster. An operator with their own CA still wins — the Secret
# is read first, and anything already there is carried through untouched.
ENROLL_ARGS=()
if $K -n "$NS" get secret cube-advisor-enrollment >/dev/null 2>&1; then
  ENROLLDIR="$(mktemp -d)"; TMPDIRS+=("$ENROLLDIR")
  for f in ca.crt ca.key tunnel.crt tunnel.key; do
    $K -n "$NS" get secret cube-advisor-enrollment -o jsonpath="{.data.${f//./\\.}}" 2>/dev/null | base64 -d > "$ENROLLDIR/$f"
  done
  [ -s "$ENROLLDIR/ca.crt" ] && [ -s "$ENROLLDIR/ca.key" ] \
    || fail "cube-advisor-enrollment exists but its CA is unreadable — refusing to upgrade and drop it"
  ENROLL_ARGS+=(--set-file enrollment.caCert="$ENROLLDIR/ca.crt" --set-file enrollment.caKey="$ENROLLDIR/ca.key")
  if [ -s "$ENROLLDIR/tunnel.crt" ] && [ -s "$ENROLLDIR/tunnel.key" ]; then
    ENROLL_ARGS+=(--set-file enrollment.tunnelCert="$ENROLLDIR/tunnel.crt" --set-file enrollment.tunnelKey="$ENROLLDIR/tunnel.key")
  fi
  echo "carrying the existing enrollment CA through the upgrade."
else
  ENROLLDIR="$(mktemp -d)"; TMPDIRS+=("$ENROLLDIR")
  # A CA that signs per-node identities, and a server keypair for the tunnel
  # listener. The tunnel SAN must carry the address agents dial: the agent pins
  # this CA and offers no flag to loosen it, so a certificate naming anything
  # else is one no agent can connect through.
  openssl ecparam -name prime256v1 -genkey -noout -out "$ENROLLDIR/ca.key" 2>/dev/null \
    || fail "could not generate the enrollment CA key"
  openssl req -x509 -new -key "$ENROLLDIR/ca.key" -sha256 -days 3650 \
    -subj "/CN=cube-advisor-enrollment" \
    -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -out "$ENROLLDIR/ca.crt" 2>/dev/null \
    || fail "could not issue the enrollment CA certificate"

  openssl ecparam -name prime256v1 -genkey -noout -out "$ENROLLDIR/tunnel.key" 2>/dev/null \
    || fail "could not generate the tunnel key"
  openssl req -new -key "$ENROLLDIR/tunnel.key" -subj "/CN=${ADVISOR_LB_IP}" \
    -out "$ENROLLDIR/tunnel.csr" 2>/dev/null \
    || fail "could not create the tunnel certificate request"
  printf 'subjectAltName=IP:%s\nextendedKeyUsage=serverAuth\nkeyUsage=critical,digitalSignature,keyEncipherment\n' \
    "$ADVISOR_LB_IP" > "$ENROLLDIR/tunnel.ext"
  openssl x509 -req -in "$ENROLLDIR/tunnel.csr" -CA "$ENROLLDIR/ca.crt" -CAkey "$ENROLLDIR/ca.key" \
    -CAcreateserial -days 3650 -sha256 -extfile "$ENROLLDIR/tunnel.ext" \
    -out "$ENROLLDIR/tunnel.crt" 2>/dev/null \
    || fail "could not issue the tunnel certificate"

  ENROLL_ARGS+=(--set-file enrollment.caCert="$ENROLLDIR/ca.crt" --set-file enrollment.caKey="$ENROLLDIR/ca.key")
  ENROLL_ARGS+=(--set-file enrollment.tunnelCert="$ENROLLDIR/tunnel.crt" --set-file enrollment.tunnelKey="$ENROLLDIR/tunnel.key")
  echo "issued an enrollment CA and a tunnel certificate for ${ADVISOR_LB_IP}."
fi

helm upgrade --install cube-advisor "oci://$RURL/$RPROJ/cube-advisor" --version "$CHART_VER" \
  -n "$NS" --create-namespace --kubeconfig "$KC" \
  --set lbIP="$ADVISOR_LB_IP" \
  --set dbPassword="$DBPW" \
  --set appDbPassword="$APPPW" \
  --set baseURL="$BASE_URL" \
  --set-file web.tls.cert="$TLSDIR/tls.crt" \
  --set-file web.tls.key="$TLSDIR/tls.key" \
  "${ENROLL_ARGS[@]}" \
  --kube-insecure-skip-tls-verify --insecure-skip-tls-verify --timeout 20m --wait=false \
  || fail "helm upgrade failed — the release is unchanged; do not uninstall to retry, that deletes the database"

# --- wait for the advisor workloads ---
echo "waiting for advisor database…"
$K -n "$NS" rollout status statefulset/cube-advisor-db --timeout=10m 2>/dev/null || fail "cube-advisor-db statefulset not ready"
echo "waiting for advisor workload…"
$K -n "$NS" rollout status deploy/cube-advisor --timeout=15m 2>/dev/null || fail "cube-advisor deployment not ready"

# --- verify: healthz + UI serving via the dedicated Advisor LB IP ---
# Octavia LB provisioning takes minutes, so poll before failing.
echo "verifying cube-advisor is serving…"
ok=""
for _ in $(seq 1 30); do
  # healthz answers "ok <version>" (bare "ok" when unversioned).
  BODY="$(curl -skf --max-time 10 "https://${ADVISOR_LB_IP}/healthz" 2>/dev/null)"
  case "$BODY" in
    ok|ok\ *) ok=1; break ;;
  esac
  sleep 10
done
[ -n "$ok" ] || fail "cube-advisor healthz not ok at https://${ADVISOR_LB_IP}/healthz"

curl -sk --max-time 20 "https://${ADVISOR_LB_IP}/" 2>/dev/null | grep -q '<div id="root">' \
  || fail "cube-advisor UI not serving at http://${ADVISOR_LB_IP}/"

# Enrollment is the point of installing this at all, and it is registered only
# when both CA halves reach the API — so assert it here rather than let the
# first cluster that tries to enrol discover a 404 months later. An
# unauthenticated POST must be refused (401), not missing (404): 404 is the
# shape a disabled enrollment endpoint has.
code="$(curl -sk --max-time 20 -o /dev/null -w '%{http_code}' \
        -X POST "https://${ADVISOR_LB_IP}/api/v1/enroll" 2>/dev/null)"
case "$code" in
  401|400) : ;;
  404) fail "cube-advisor is serving but enrollment is disabled (POST /api/v1/enroll -> 404) — the CA never reached the API, so no cluster can enrol" ;;
  *)   echo "warning: POST /api/v1/enroll answered $code; expected 401" >&2 ;;
esac

echo "cube-advisor installed and verified: https://${ADVISOR_LB_IP}/"
