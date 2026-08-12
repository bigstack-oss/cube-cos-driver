#!/bin/bash
# Deploy the CubeCMP portal into an app-framework and verify it end-to-end.
# app_register only pushes the chart + prereqs to Harbor; this installs the chart,
# works around the post-install DB-migration race, and confirms the portal serves
# and the admin permission was granted. Idempotent: safe to re-run.
#
# args: <framework> <lb_ip> <chart_version>
set -uo pipefail
FRAMEWORK="${1:?framework name required}"
LBIP="${2:?lb ip required}"
CHART_VER="${3:?chart version required}"
NS=cube-portal

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
KC="/tmp/${FRAMEWORK}-portal.kc"
sudo /usr/local/bin/rancher cluster kf "$FRAMEWORK" 2>/dev/null > "$KC"
SRV="$(kubectl config view --kubeconfig="$KC" -o jsonpath='{.clusters[0].cluster.server}')"
[ -n "$SRV" ] || fail "empty framework kubeconfig (rancher login/kf failed)"
kubectl config set-cluster "$(kubectl config view --kubeconfig="$KC" -o jsonpath='{.clusters[0].name}')" \
  --server="$(echo "$SRV" | sed "s#https://[^/]*#https://${CTRL}:10443#")" --kubeconfig="$KC" >/dev/null
K="kubectl --insecure-skip-tls-verify --kubeconfig=$KC"

# --- install the chart (skip if already present) ---
if $K -n "$NS" get deploy portal >/dev/null 2>&1; then
  echo "cube-portal already installed — skipping helm install."
else
  RURL="$($K -n harbor get secret registry-details -o jsonpath='{.data.registryUrl}' 2>/dev/null | base64 -d)"
  RPROJ="$($K -n harbor get secret registry-details -o jsonpath='{.data.registryExtensionProject}' 2>/dev/null | base64 -d)"
  RUSER="$($K -n harbor get secret registry-details -o jsonpath='{.data.registryServiceAccount}' 2>/dev/null | base64 -d)"
  RPASS="$($K -n harbor get secret registry-details -o jsonpath='{.data.registryServicePassword}' 2>/dev/null | base64 -d)"
  [ -n "$RURL" ] || fail "registry-details.registryUrl is empty — appctl's registry setup did not complete"
  echo "installing cube-portal $CHART_VER from oci://$RURL/$RPROJ …"
  grep -q "$RURL" /etc/hosts || echo "$LBIP $RURL" | sudo tee -a /etc/hosts >/dev/null
  helm registry login "$RURL" -u "$RUSER" -p "$RPASS" --insecure >/dev/null 2>&1
  # app_register can exit 0 having pushed nothing if the framework registry
  # wasn't set up — fail clearly here, not on a cryptic helm pull.
  helm show chart "oci://$RURL/$RPROJ/cube-portal" --version "$CHART_VER" --insecure-skip-tls-verify >/dev/null 2>&1 \
    || fail "cube-portal chart not in registry (oci://$RURL/$RPROJ/cube-portal:$CHART_VER) — app_register pushed nothing; check the framework registry setup"
  # Older cubecmp charts ship rancherToken + worker.keycloak.admin empty, which
  # breaks login and the resyncer; set them so the portal works from scratch.
  [ -n "$RT" ] || fail "rancher token unavailable; portal would deploy without RANCHER_TOKEN"
  # post-install hook can fail on a DB-not-ready race; don't let that abort us.
  # Pin the portal URL to the LB IP; unset, the chart reads it from the ingress-lb
  # service at render time and can freeze <pending>. See cube-cos-app-framework#39.
  helm install cube-portal "oci://$RURL/$RPROJ/cube-portal" --version "$CHART_VER" \
    -n "$NS" --create-namespace --kubeconfig "$KC" \
    --set rancherToken="$RT" \
    --set serverUrl="https://${LBIP}" \
    --set keycloakUrl="https://${LBIP}/auth/realms/master" \
    --set worker.keycloak.admin.username=admin \
    --set worker.keycloak.admin.password=admin \
    --kube-insecure-skip-tls-verify --insecure-skip-tls-verify --timeout 25m --wait=false || true
  sed -i "/$RURL/d" /etc/hosts
fi

# --- wait for the databases, then ensure the post-install hook completes ---
echo "waiting for databases…"
$K -n "$NS" wait --for=condition=ready pod cube-portal-mongodb-0 --timeout=8m 2>/dev/null || true
$K -n "$NS" wait --for=condition=ready pod cube-portal-postgresql-ha-postgresql-0 --timeout=8m 2>/dev/null || true

# Wait for the post-install job's terminal Complete/Failed condition and re-run
# only on Failed (steps are idempotent). Keying off .status.failed would also
# count a backoffLimit>0 chart's in-progress retries and re-run a job that
# later succeeds.
rerun=""
for _ in $(seq 1 60); do
  comp="$($K -n "$NS" get job cube-portal-post-install -o jsonpath='{.status.conditions[?(@.type=="Complete")].status}' 2>/dev/null)"
  failed="$($K -n "$NS" get job cube-portal-post-install -o jsonpath='{.status.conditions[?(@.type=="Failed")].status}' 2>/dev/null)"
  [ "$comp" = "True" ] && break
  [ "$failed" = "True" ] && { rerun=1; break; }
  sleep 10
done
if [ -n "$rerun" ]; then
  echo "post-install job failed (DB race) — re-running…"
  $K -n "$NS" delete job post-install-rerun --ignore-not-found >/dev/null 2>&1
  $K -n "$NS" get job cube-portal-post-install -o json 2>/dev/null | python3 -c '
import sys,json
j=json.load(sys.stdin)
j["metadata"]={"name":"post-install-rerun","namespace":"cube-portal"}
j["spec"].pop("selector",None)
j["spec"]["template"]["metadata"].pop("labels",None)
j["status"]={}
print(json.dumps(j))' | $K apply -f - >/dev/null
  $K -n "$NS" wait --for=condition=complete job/post-install-rerun --timeout=10m 2>/dev/null || fail "post-install re-run did not complete"
fi

# --- wait for the portal workloads ---
echo "waiting for portal workloads…"
$K -n "$NS" rollout restart deploy/portal-api >/dev/null 2>&1 || true
# 20m, not 8m: on a fresh air-gapped cluster the portal pods wait on image
# pulls + a manila-backed nfs-vol mount, which can exceed 8m.
$K -n "$NS" rollout status deploy/portal --timeout=20m 2>/dev/null || fail "portal deployment not ready"
$K -n "$NS" rollout status deploy/portal-api --timeout=20m 2>/dev/null || fail "portal-api deployment not ready"

# --- verify: portal serves + admin permission granted ---
echo "verifying portal is serving…"
CODE="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 20 "https://${LBIP}/portal" 2>/dev/null)"
[ "$CODE" = "200" ] || fail "portal not serving (HTTP $CODE) at https://${LBIP}/portal"

echo "verifying admin permission…"
KADM="$K -n keycloak exec keycloak-0 -- /opt/jboss/keycloak/bin/kcadm.sh"
$KADM config credentials --server http://localhost:8080/auth --realm master --user admin --password admin >/dev/null 2>&1
ATTR="$($KADM get users -r master -q username=admin 2>/dev/null | python3 -c 'import sys,json;print(json.load(sys.stdin)[0].get("attributes",{}).get("ProjectRole",[""])[0])' 2>/dev/null)"
[ "$ATTR" = "USER_DEFINED_DEFAULT-admin" ] || fail "admin ProjectRole not granted (got '$ATTR')"

echo "cube-portal installed and verified: https://${LBIP}/portal (admin permission granted)"
