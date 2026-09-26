#!/bin/bash
# Deploy Cube AI Advisor into an app-framework and verify it end-to-end.
# advisor_register only pushes the chart + prereqs to Harbor; this installs
# the chart and confirms the advisor serves. Idempotent: safe to re-run.
#
# args: <framework> <advisor_lb_ip> <chart_version> [base_url] [console_pool]
#       [console_account] [provider_key_file] [provider_url]
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
# Comma-separated addresses for the web console's origins, one per distinct
# upstream it proxies to. Separate addresses rather than ports or paths:
# browsers separate cookie jars by host and by nothing else (RFC 6265 ignores
# port), and a path scheme needs URL rewriting that breaks the OIDC flow.
# Empty leaves the console off, which is what every install did before.
CONSOLE_POOL="${5:-}"
# The login account console certificates authorise on a node. CubeCOS
# provisions "advisor" for exactly this, so that is the default; empty leaves
# the console disabled, which is what every install did before.
CONSOLE_ACCOUNT="${6:-advisor}"
# The inference endpoint the chat surface calls, and a file holding its key.
# Both optional: given, they set it; omitted, whatever the deployment already
# uses is carried through.
PROVIDER_URL="${8:-}"
PROVIDER_MODEL="${9:-}"
NS=cube-advisor

fail() { echo "ERROR: $*" >&2; exit 1; }

# One cleanup for one EXIT trap. Each `trap … EXIT` replaces the last, so the
# separate traps this script used to set meant only the final one ran and the
# /etc/hosts pin leaked — exactly what its own comment says must not happen.
HOSTS_PIN=""; TMPDIRS=()
cleanup() {
  [ -n "$HOSTS_PIN" ] && sudo sed -i "/${HOSTS_PIN}/d" /etc/hosts
  # The staged provider key is for this run only.
  [ -n "${7:-}" ] && rm -f "${7}"
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
#
# Every console origin address is a SAN too. advisor-api refuses to enable the
# web console when its certificate does not cover an origin, naming the
# uncovered one -- so an address left out here is a console that does not
# start. A *reused* certificate predates any address added since it was issued,
# which is why the pool is asked for whole up front and why a reuse that does
# not cover it is refused below rather than quietly installed.
SANS="IP:${ADVISOR_LB_IP}"
POOL_ADDRS=()
if [ -n "$CONSOLE_POOL" ]; then
  IFS=, read -ra POOL_ADDRS <<< "$CONSOLE_POOL"
  for a in "${POOL_ADDRS[@]}"; do
    [ -n "$a" ] && SANS="$SANS,IP:$a"
  done
fi

TLSDIR="$(mktemp -d)"; TMPDIRS+=("$TLSDIR")
if $K -n "$NS" get secret cube-advisor-web-tls >/dev/null 2>&1; then
  $K -n "$NS" get secret cube-advisor-web-tls -o jsonpath='{.data.tls\.crt}' | base64 -d > "$TLSDIR/tls.crt"
  $K -n "$NS" get secret cube-advisor-web-tls -o jsonpath='{.data.tls\.key}' | base64 -d > "$TLSDIR/tls.key"
fi
if [ ! -s "$TLSDIR/tls.crt" ] || [ ! -s "$TLSDIR/tls.key" ]; then
  openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
    -subj "/CN=${ADVISOR_LB_IP}" -addext "subjectAltName=${SANS}" \
    -keyout "$TLSDIR/tls.key" -out "$TLSDIR/tls.crt" 2>/dev/null \
    || fail "could not issue the advisor's TLS certificate"
elif [ "${#POOL_ADDRS[@]}" -gt 0 ]; then
  for a in "${POOL_ADDRS[@]}"; do
    [ -n "$a" ] || continue
    openssl x509 -in "$TLSDIR/tls.crt" -noout -text 2>/dev/null | grep -q "IP Address:$a\b" \
      || fail "the advisor's existing certificate does not cover console address $a — delete the cube-advisor-web-tls secret to reissue it (every browser that has accepted this advisor will challenge it once more)"
  done
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

# --- web console origins ---
# One origin per distinct upstream: advisor-api refuses a config where two
# origins share one, and CMP and its Keycloak genuinely share the framework
# ingress -- they MUST land on one origin, or the OIDC state cookie is set on
# one and the callback arrives at the other and login fails.
#
# Target names are the node's, not ours: cubecos allows cube-cmp, app-fw-idp
# and cube-cos, and a name the node does not allow is a target the agent
# refuses to dial.
WC_ARGS=()
if [ "${#POOL_ADDRS[@]}" -ge 2 ]; then
  INGRESS="$($K get svc --all-namespaces --field-selector metadata.name=ingress-lb \
             -o jsonpath='{.items[0].status.loadBalancer.ingress[0].ip}' 2>/dev/null)"
  WC_ARGS+=(--set webConsole.enabled=true --set webConsole.mode=address)
  n=0
  if [ -n "$INGRESS" ]; then
    # /portal, not /: the framework ingress serves the portal there and
    # Keycloak under /auth, and claims nothing at the root.
    WC_ARGS+=(--set "webConsole.origins[$n].address=${POOL_ADDRS[$n]}" \
              --set "webConsole.origins[$n].upstream=https://$INGRESS" \
              --set "webConsole.origins[$n].path=/portal" \
              --set "webConsole.origins[$n].targets[0]=cube-cmp" \
              --set "webConsole.origins[$n].targets[1]=app-fw-idp")
    n=$((n+1))
  else
    echo "warning: no ingress-lb on framework $FRAMEWORK; the CMP console origin is not configured" >&2
  fi
  # The node's own dashboard, at the control VIP rather than loopback:
  # 127.0.0.1:8080 is httpd, which answers 403 to everything.
  #
  # The endpoints it links out to are companions rather than origins of their
  # own: the dashboard sends the browser straight at them, so they must be
  # reachable from one session, and they share its address and its LB.
  #
  # Keystone is here because Skyline's federated login leaves Skyline for it.
  # :5000 is the one plain-HTTP upstream, hence a scheme per entry.
  # Labels and hidden match the dashboard's /integrations/applications list;
  # the two keystone entries are SSO plumbing, so hidden.
  WC_ARGS+=(--set "webConsole.origins[$n].address=${POOL_ADDRS[$n]}" \
            --set "webConsole.origins[$n].upstream=https://$CTRL" \
            --set "webConsole.origins[$n].label=CubeCOS" \
            --set "webConsole.origins[$n].targets[0]=cube-cos")
  c=0
  # Fields: scheme|port|target|label|hidden|path. Ceph alone needs a path.
  for spec in "https|10443|cube-cos-idp|Rancher||" \
              "https|9999|cube-cos-skyline|OpenStack||" \
              "https|7443|cube-cos-ceph|Ceph||/ceph/" \
              "http|5000|cube-cos-keystone||hidden|" \
              "https|5443|cube-cos-keystone-sso||hidden|"; do
    IFS='|' read -r scheme port name label hidden path <<<"$spec"
    WC_ARGS+=(--set "webConsole.origins[$n].companions[$c].address=${POOL_ADDRS[$n]}:$port" \
              --set "webConsole.origins[$n].companions[$c].upstream=$scheme://$CTRL:$port" \
              --set "webConsole.origins[$n].companions[$c].targets[0]=$name")
    [ -n "$label" ] && WC_ARGS+=(--set "webConsole.origins[$n].companions[$c].label=$label")
    [ -n "$hidden" ] && WC_ARGS+=(--set "webConsole.origins[$n].companions[$c].hidden=true")
    [ -n "$path" ] && WC_ARGS+=(--set "webConsole.origins[$n].companions[$c].path=$path")
    # Keycloak shares :10443 with Rancher, so it gets a named link.
    if [ "$name" = "cube-cos-idp" ]; then
      WC_ARGS+=(--set "webConsole.origins[$n].companions[$c].links[0].label=Keycloak" \
                --set "webConsole.origins[$n].companions[$c].links[0].path=/auth/admin")
    fi
    c=$((c+1))
  done
  # Nothing to tell the cluster about the origins: the Advisor reports them to
  # every agent on connect and each node applies them itself.
  echo "web console enabled on ${#POOL_ADDRS[@]} origin address(es)."
elif [ -n "$CONSOLE_POOL" ]; then
  echo "warning: the console pool has fewer than 2 addresses; leaving the web console off" >&2
fi

# --- the inference provider ---
# Carried through a re-run, like the TLS and enrollment material above and for
# the same reason: this script renders the whole release, so a value it does
# not pass back is one helm removes.
PROVIDER_ARGS=()
PROVIDER_KEY_FILE="${7:-}"
if [ -n "$PROVIDER_KEY_FILE" ]; then
  [ -r "$PROVIDER_KEY_FILE" ] || fail "cannot read the provider key file: $PROVIDER_KEY_FILE"
  PROVIDER_ARGS+=(--set-file provider.key="$PROVIDER_KEY_FILE")
  [ -n "$PROVIDER_URL" ] && PROVIDER_ARGS+=(--set provider.url="$PROVIDER_URL")
  [ -n "$PROVIDER_MODEL" ] && PROVIDER_ARGS+=(--set provider.model="$PROVIDER_MODEL")
  echo "provider set to ${PROVIDER_URL:-the chart default}${PROVIDER_MODEL:+, model $PROVIDER_MODEL}."
else
  # Read back what the deployment is already using. The key lives in the
  # Secret; the URL is an argument on the container.
  PREV_KEY="$($K -n "$NS" get secret cube-advisor-secrets -o jsonpath='{.data.providerKey}' 2>/dev/null | base64 -d)"
  # By position in the argument list: a jsonpath range piped through grep
  # silently produced nothing, carrying the key while the URL fell back.
  PREV_URL="$($K -n "$NS" get deploy cube-advisor -o json 2>/dev/null | jq -r '
    .spec.template.spec.containers[0].args as $a
    | ($a | index("-provider-url")) as $i
    | if $i == null then empty else $a[$i + 1] end')"
  if [ -n "$PREV_KEY" ]; then
    PROVIDER_KEEP="$(mktemp)"; TMPDIRS+=("$PROVIDER_KEEP")
    printf '%s' "$PREV_KEY" > "$PROVIDER_KEEP"
    PROVIDER_ARGS+=(--set-file provider.key="$PROVIDER_KEEP")
    echo "carrying the existing provider key through the upgrade."
  fi
  if [ -n "$PREV_URL" ]; then
    PROVIDER_ARGS+=(--set provider.url="$PREV_URL")
  fi
  PREV_MODEL="$($K -n "$NS" get deploy cube-advisor -o json 2>/dev/null | jq -r '
    .spec.template.spec.containers[0].args as $a
    | ($a | index("-provider-model")) as $i
    | if $i == null then empty else $a[$i + 1] end')"
  if [ -n "$PREV_MODEL" ]; then
    PROVIDER_ARGS+=(--set provider.model="$PREV_MODEL")
  fi
fi

# --- the console's SSH user CA ---
# advisor-api generates one at startup when the chart passes no key, which
# suits a dev run and breaks a deployment: a node pins this CA in sshd and
# keeps it across firmware upgrades, so a CA regenerated on the next pod
# restart silently stops every already-enrolled node accepting console
# certificates. Generated once here and passed back on every upgrade, exactly
# like the enrollment CA above.
CONSOLE_ARGS=()
CONSOLEDIR="$(mktemp -d)"; TMPDIRS+=("$CONSOLEDIR")
if $K -n "$NS" get secret cube-advisor-console-ca >/dev/null 2>&1; then
  $K -n "$NS" get secret cube-advisor-console-ca -o jsonpath='{.data.ca\.key}' 2>/dev/null | base64 -d > "$CONSOLEDIR/ca.key"
  [ -s "$CONSOLEDIR/ca.key" ] \
    || fail "cube-advisor-console-ca exists but its key is unreadable — refusing to upgrade and strand every node that trusts it"
  echo "carrying the existing console CA through the upgrade."
else
  # ed25519: what console/ca.go generates for itself, and what sshd expects in
  # a TrustedUserCAKeys line.
  ssh-keygen -q -t ed25519 -N "" -C cube-advisor-console -f "$CONSOLEDIR/ca.key" \
    || fail "could not generate the console CA"
  echo "issued a console CA for account ${CONSOLE_ACCOUNT}."
fi
CONSOLE_ARGS+=(--set console.account="$CONSOLE_ACCOUNT" --set-file console.caKey="$CONSOLEDIR/ca.key")

helm upgrade --install cube-advisor "oci://$RURL/$RPROJ/cube-advisor" --version "$CHART_VER" \
  -n "$NS" --create-namespace --kubeconfig "$KC" \
  --set lbIP="$ADVISOR_LB_IP" \
  --set dbPassword="$DBPW" \
  --set appDbPassword="$APPPW" \
  --set baseURL="$BASE_URL" \
  --set-file web.tls.cert="$TLSDIR/tls.crt" \
  --set-file web.tls.key="$TLSDIR/tls.key" \
  "${ENROLL_ARGS[@]}" \
  "${WC_ARGS[@]}" \
  "${CONSOLE_ARGS[@]}" \
  "${PROVIDER_ARGS[@]}" \
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

# Polled, like healthz above. healthz answers from the pod as soon as it is
# ready, but the Octavia LB can still be settling the listener for the root
# path a moment later -- so a single unretried request here failed installs
# whose UI was serving fine seconds afterwards.
served=""
for _ in $(seq 1 12); do
  if curl -sk --max-time 20 "https://${ADVISOR_LB_IP}/" 2>/dev/null | grep -q '<div id="root">'; then
    served=1
    break
  fi
  sleep 5
done
[ -n "$served" ] || fail "cube-advisor UI not serving at https://${ADVISOR_LB_IP}/"

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

# --- the first administrator ---
# Migrations deliberately seed no account (the hosted service must not ship
# admin/admin); the offline installer is where the first one is made. Local
# sign-in is otherwise impossible, and a deployment nobody can sign in to is
# not installed. The account can do nothing but replace its password.
if $K -n "$NS" exec deploy/cube-advisor -c api -- advisorctl local init \
     -dsn "postgres://postgres:${DBPW}@cube-advisor-db:5432/advisor?sslmode=disable" >/dev/null 2>&1; then
  # Idempotent: an account that already exists is left as it is, password
  # included — so on a re-run this line is only true if nobody changed it.
  echo "first administrator: admin / admin (unless already changed) — the UI asks for a new password at first sign-in"
else
  echo "warning: could not create the first administrator; run 'advisorctl local init' in the api pod" >&2
fi

# The node half of the console: sshd has to trust this CA before a console
# session can authenticate, and nothing pushes it — the Advisor mints
# certificates, the node decides whether to accept them. Print it where the
# operator installing this will see it.
if [ -s "$CONSOLEDIR/ca.key.pub" ]; then
  echo
  echo "Console CA for account ${CONSOLE_ACCOUNT}. On each node, run:"
  echo "  hex_cli -c advisor -c console_trust <file containing the line below>"
  cat "$CONSOLEDIR/ca.key.pub"
  echo
fi

echo "cube-advisor installed and verified: https://${ADVISOR_LB_IP}/"
