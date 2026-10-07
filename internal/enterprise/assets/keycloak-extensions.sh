#!/bin/bash
# Add CubeCMP's Keycloak extensions to the app-framework Keycloak.
# usage: keycloak-extensions.sh <bundle.tgz>   (jars -> deployments/, *.ftl -> cube login theme)
set -euo pipefail

tgz=$1
K="kubectl --kubeconfig=/opt/appfw/kubeconfig -n keycloak"
CM=keycloak-cmp-extensions
VOL=cmp-ext
DEPLOY=/opt/jboss/keycloak/standalone/deployments
THEME=/opt/jboss/keycloak/themes/cube/login

want=$(md5sum "$tgz" | awk '{print $1}')
have=$($K get sts keycloak -o jsonpath='{.spec.template.metadata.annotations.cube\.io/cmp-extensions}' 2>/dev/null || true)
if [ "$have" = "$want" ]; then
    echo "Keycloak already carries these CubeCMP extensions (${want}) — nothing to do."
    exit 0
fi

dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
tar -xzf "$tgz" -C "$dir"
jars=$(cd "$dir" && ls *.jar 2>/dev/null || true)
ftls=$(cd "$dir" && ls *.ftl 2>/dev/null || true)
[ -n "$jars" ] || { echo "ERROR: $tgz holds no .jar" >&2; exit 1; }

echo "Staging $(echo $jars $ftls | wc -w) file(s) as configmap $CM…"
$K create configmap $CM --from-file="$dir" --dry-run=client -o yaml | $K apply -f -

container=$($K get sts keycloak -o jsonpath='{.spec.template.spec.containers[0].name}')
mounts=""
for f in $jars; do mounts="$mounts{\"name\":\"$VOL\",\"mountPath\":\"$DEPLOY/$f\",\"subPath\":\"$f\"},"; done
for f in $ftls; do mounts="$mounts{\"name\":\"$VOL\",\"mountPath\":\"$THEME/$f\",\"subPath\":\"$f\"},"; done
patch="{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{\"cube.io/cmp-extensions\":\"$want\"}},\"spec\":{\"volumes\":[{\"name\":\"$VOL\",\"configMap\":{\"name\":\"$CM\"}}],\"containers\":[{\"name\":\"$container\",\"volumeMounts\":[${mounts%,}]}]}}}}"
echo "Mounting them into Keycloak and rolling it…"
$K patch sts keycloak --type=strategic -p "$patch"
$K rollout status sts keycloak --timeout=600s

# WildFly's deployment scanner marks each jar <jar>.deployed once it's loaded.
for f in $jars; do
    for _ in $(seq 1 60); do
        $K exec keycloak-0 -c "$container" -- test -e "$DEPLOY/$f.deployed" 2>/dev/null && break
        sleep 5
    done
    $K exec keycloak-0 -c "$container" -- test -e "$DEPLOY/$f.deployed" ||
        { echo "ERROR: Keycloak did not deploy $f (no $f.deployed after 5m)" >&2; exit 1; }
    echo "  ✓ $f deployed"
done
echo "CubeCMP Keycloak extensions in place."
