# --- rancher login + framework kubeconfig ---
# Spliced into the install/uninstall scripts at embed time (see assets.go).
# Needs FRAMEWORK, CTRL, RT, KC and fail(); leaves the kubeconfig in $KC.
RANCHER=/usr/local/bin/rancher
RANCHER_URL="https://${CTRL}:10443"
RANCHER_CA="${RANCHER_CA:-/var/www/certs/server.pem}"
# CLI 2.12 dropped --skip-verify: use the node CA, else --skip-verify if the CLI has it.
if [ -s "$RANCHER_CA" ]; then
  RANCHER_TLS=(--cacert "$RANCHER_CA"); CURL_TLS=(--cacert "$RANCHER_CA")
elif sudo "$RANCHER" login --help 2>&1 | grep -q -- '--skip-verify'; then
  RANCHER_TLS=(--skip-verify); CURL_TLS=(-k)
else
  fail "no rancher CA at $RANCHER_CA and this rancher CLI has no --skip-verify"
fi
rancher_api() {
  curl -sSf "${CURL_TLS[@]}" --max-time 30 -H "Authorization: Bearer $RT" "${RANCHER_URL}$1"
}
# Select the framework's own cluster/project; never the ~/.rancher context.
CLUSTER_ID="$(rancher_api "/v3/clusters?name=${FRAMEWORK}" | jq -r '.data[0].id // empty')"
[ -n "$CLUSTER_ID" ] || fail "rancher cluster '$FRAMEWORK' not found at $RANCHER_URL"
RANCHER_CTX="$(rancher_api "/v3/projects?clusterId=${CLUSTER_ID}&name=Default" | jq -r '.data[0].id // empty')"
[ -n "$RANCHER_CTX" ] || fail "no Default project in rancher cluster '$FRAMEWORK' ($CLUSTER_ID)"
# Private config dir so the shared ~/.rancher/cli2.json is neither read nor rewritten.
RANCHER_CFG="$(mktemp -d)"
rancher_cli() { sudo "$RANCHER" --config "$RANCHER_CFG" "$@"; }
if ! RANCHER_OUT="$(rancher_cli login "${RANCHER_TLS[@]}" --token "$RT" \
    --context "$RANCHER_CTX" "$RANCHER_URL" </dev/null 2>&1)"; then
  sudo rm -rf "$RANCHER_CFG"
  fail "rancher login to $RANCHER_URL (context $RANCHER_CTX) failed: $RANCHER_OUT"
fi
RANCHER_OUT="$(rancher_cli cluster kf "$CLUSTER_ID" 2>&1 >"$KC")"
sudo rm -rf "$RANCHER_CFG"
SRV="$(kubectl config view --kubeconfig="$KC" -o jsonpath='{.clusters[0].cluster.server}')"
[ -n "$SRV" ] || fail "empty framework kubeconfig for cluster '$FRAMEWORK' ($CLUSTER_ID, context $RANCHER_CTX): $RANCHER_OUT"
