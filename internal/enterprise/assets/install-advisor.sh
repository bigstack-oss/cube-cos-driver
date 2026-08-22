#!/bin/bash
# Deploy Cube AI Advisor into an app-framework and verify it end-to-end.
# advisor_register only pushes the chart + prereqs to Harbor; this installs
# the chart and confirms the advisor serves. Idempotent: safe to re-run.
#
# args: <framework> <advisor_lb_ip> <chart_version>
set -uo pipefail
FRAMEWORK="${1:?framework name required}"
ADVISOR_LB_IP="${2:?advisor lb ip required}"
CHART_VER="${3:?chart version required}"
NS=cube-advisor

fail() { echo "ERROR: $*" >&2; exit 1; }

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

# --- install the chart (skip if already present) ---
if $K -n "$NS" get deploy cube-advisor >/dev/null 2>&1; then
  echo "cube-advisor already installed — skipping helm install."
else
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
    trap 'sudo sed -i "/${RURL}/d" /etc/hosts' EXIT
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
    HSSEC=$($K -n "$NS" get secret cube-advisor-secrets -o jsonpath='{.data.hs256Secret}' | base64 -d)
  else
    DBPW=$(openssl rand -hex 16); APPPW=$(openssl rand -hex 16); HSSEC=$(openssl rand -hex 24)
  fi

  helm upgrade --install cube-advisor "oci://$RURL/$RPROJ/cube-advisor" --version "$CHART_VER" \
    -n "$NS" --create-namespace --kubeconfig "$KC" \
    --set lbIP="$ADVISOR_LB_IP" \
    --set dbPassword="$DBPW" \
    --set appDbPassword="$APPPW" \
    --set hs256Secret="$HSSEC" \
    --kube-insecure-skip-tls-verify --insecure-skip-tls-verify --timeout 20m --wait=false
fi

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
  BODY="$(curl -skf --max-time 10 "http://${ADVISOR_LB_IP}/healthz" 2>/dev/null)"
  case "$BODY" in
    ok|ok\ *) ok=1; break ;;
  esac
  sleep 10
done
[ -n "$ok" ] || fail "cube-advisor healthz not ok at http://${ADVISOR_LB_IP}/healthz"

curl -sk --max-time 20 "http://${ADVISOR_LB_IP}/" 2>/dev/null | grep -q '<div id="root">' \
  || fail "cube-advisor UI not serving at http://${ADVISOR_LB_IP}/"

echo "cube-advisor installed and verified: http://${ADVISOR_LB_IP}/"
