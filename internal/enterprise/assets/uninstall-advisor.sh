#!/bin/bash
# Uninstall Cube AI Advisor from an app-framework: helm-uninstall the release
# and delete its namespace. Leaves the App-Framework, the pushed chart, and the
# imported images in place. Idempotent: a no-op if the advisor isn't installed.
#
# args: <framework>
#
# NOTE: this leaves Octavia LB removal to the Service deletion (cascading from
# the namespace delete below); if the ingress-lb Service's finalizer doesn't
# clean it up, the LB is leaked and shows up in `lb_sweep`.
set -uo pipefail
FRAMEWORK="${1:?framework name required}"
NS=cube-advisor

fail() { echo "ERROR: $*" >&2; exit 1; }

# --- framework kubeconfig (rancher-proxied, rewritten to the control VIP) ---
CTRL="$(grep 'cubesys.control.vip' /etc/settings.txt | cut -d= -f2 | tr -d ' ')"
if [ -z "$CTRL" ]; then
  MGMT="$(grep 'cubesys.management' /etc/settings.txt | cut -d= -f2 | tr -d ' ')"
  CTRL="$(grep "net.if.addr.${MGMT}" /etc/settings.txt | cut -d= -f2 | tr -d ' ')"
fi
RT="$(terraform-cube.sh state pull 2>/dev/null | sed -n '/^{/,$p' | jq -r '.resources[]|select(.type=="rancher2_bootstrap").instances[0].attributes.token')"
[ -n "$RT" ] || fail "could not read rancher token"
# --skip-verify suppresses the trust prompt; feed "1" for the project prompt so
# login persists non-interactively (the picked project is irrelevant to `kf`).
yes 1 | sudo /usr/local/bin/rancher login --skip-verify --token "$RT" "https://${CTRL}:10443" >/dev/null 2>&1
KC="/tmp/${FRAMEWORK}-advisor.kc"
sudo /usr/local/bin/rancher cluster kf "$FRAMEWORK" 2>/dev/null > "$KC"
SRV="$(kubectl config view --kubeconfig="$KC" -o jsonpath='{.clusters[0].cluster.server}')"
[ -n "$SRV" ] || fail "empty framework kubeconfig (rancher login/kf failed)"
kubectl config set-cluster "$(kubectl config view --kubeconfig="$KC" -o jsonpath='{.clusters[0].name}')" \
  --server="$(echo "$SRV" | sed "s#https://[^/]*#https://${CTRL}:10443#")" --kubeconfig="$KC" >/dev/null
K="kubectl --insecure-skip-tls-verify --kubeconfig=$KC"

# --- uninstall the chart ---
if helm status cube-advisor -n "$NS" --kubeconfig "$KC" >/dev/null 2>&1; then
  echo "uninstalling cube-advisor from namespace $NS…"
  helm uninstall cube-advisor -n "$NS" --kubeconfig "$KC" --wait --timeout 10m || true
else
  echo "cube-advisor helm release not found — skipping helm uninstall."
fi

# --- delete the namespace (removes the DB, PVCs, and secrets the chart left
# behind). Issue the delete async, then force-terminate any pods left
# Terminating: a slow volume detach otherwise keeps the namespace in
# Terminating for many minutes and the step looks stuck. Bounded to ~2 min.
if $K get namespace "$NS" >/dev/null 2>&1; then
  echo "deleting namespace $NS…"
  $K delete namespace "$NS" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  for _ in $(seq 1 24); do
    $K get namespace "$NS" >/dev/null 2>&1 || break
    pods=$($K get pods -n "$NS" -o name 2>/dev/null)
    [ -n "$pods" ] && $K delete $pods -n "$NS" --force --grace-period=0 >/dev/null 2>&1 || true
    sleep 5
  done
  $K get namespace "$NS" >/dev/null 2>&1 && echo "namespace $NS still terminating (detach slow); continuing." || echo "namespace $NS deleted."
fi

echo "cube-advisor uninstalled from framework $FRAMEWORK (App-Framework left intact)."
