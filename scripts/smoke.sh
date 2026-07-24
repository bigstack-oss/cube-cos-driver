#!/bin/bash
# End-to-end smoke test: build everything, run the binary, exercise the API,
# and verify pxe export behavior.
set -euo pipefail
cd "$(dirname "$0")/.."

PORT=${SMOKE_PORT:-3299}
TMP=$(mktemp -d)
trap 'kill $SRV_PID 2>/dev/null || true; rm -rf "$TMP"' EXIT

make all

./bin/cube-cos-snapshot --port "$PORT" --data-dir "$TMP/data" --export-dir "$TMP/export" &
SRV_PID=$!

for i in $(seq 1 30); do
  curl -sf "http://localhost:$PORT/healthz" >/dev/null && break
  sleep 0.2
done

fail() { echo "SMOKE FAIL: $1" >&2; exit 1; }

# SPA served with hashed assets (not the placeholder).
curl -sf "http://localhost:$PORT/" | grep -q '/assets/index-' || fail "SPA not embedded"

# PUT the ha3 fixture.
ID=aabbccddee01
curl -sf -X PUT -H 'Content-Type: application/json' \
  --data @internal/generator/testdata/fixtures/ha3.json \
  "http://localhost:$PORT/api/v1/clusters/$ID" >/dev/null || fail "PUT cluster"

# List + detail.
curl -sf "http://localhost:$PORT/api/v1/clusters" | grep -q '"sky-lab"' || fail "list"
curl -sf "http://localhost:$PORT/api/v1/clusters/$ID" | grep -q '"cube-2"' || fail "detail"

# Downloads.
curl -sf -o "$TMP/cluster.zip" "http://localhost:$PORT/api/v1/clusters/$ID/download" || fail "zip download"
unzip -l "$TMP/cluster.zip" | grep -q "$ID/cube-1.snapshot" || fail "zip content"
curl -sf -o "$TMP/node.snapshot" "http://localhost:$PORT/api/v1/clusters/$ID/nodes/cube-2/download" || fail "node download"
unzip -p "$TMP/node.snapshot" Comment | grep -q '^Generated for sky-lab' || fail "snapshot Comment"
unzip -p "$TMP/node.snapshot" etc/policies/cubesys/cubesys1_0.yml | grep -q 'control-vip: 10.254.0.100' || fail "cubesys content"

# PXE export dir: flat <hostname>.snapshot files for `snapshot pull url`.
for h in cube-1 cube-2 cube-3; do
  [ -f "$TMP/export/$h.snapshot" ] || fail "export $h.snapshot"
done

# Delete removes exports too.
curl -sf -X DELETE "http://localhost:$PORT/api/v1/clusters/$ID" || fail "delete"
[ ! -f "$TMP/export/cube-1.snapshot" ] || fail "stale export after delete"

# Machines (hardware inventory): create, list without secret leak, delete.
MID=$(curl -sf -X POST -H 'Content-Type: application/json' \
  -d '{"label":"sky141","bmc":{"address":"10.32.140.141","username":"admin","password":"secret"}}' \
  "http://localhost:$PORT/api/v1/machines" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
[ -n "$MID" ] || fail "create machine"
LIST=$(curl -sf "http://localhost:$PORT/api/v1/machines")
echo "$LIST" | grep -q '"sky141"' || fail "machine list"
echo "$LIST" | grep -q '"hasPassword":true' || fail "hasPassword flag"
echo "$LIST" | grep -q 'secret' && fail "machine list leaks password"
# Password is encrypted on disk, never plaintext.
grep -rq 'secret' "$TMP/data/machines" && fail "plaintext password on disk"
grep -rq 'passwordEnc' "$TMP/data/machines" || fail "no encrypted password on disk"
curl -sf -X DELETE "http://localhost:$PORT/api/v1/machines/$MID" || fail "delete machine"

echo "SMOKE PASS"
