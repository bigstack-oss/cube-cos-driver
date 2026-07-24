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

echo "SMOKE PASS"
