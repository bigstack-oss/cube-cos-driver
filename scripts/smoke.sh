#!/bin/bash
# End-to-end smoke test: build everything, run the binary, exercise the API,
# and verify pxe export behavior.
set -euo pipefail
cd "$(dirname "$0")/.."

PORT=${SMOKE_PORT:-3299}
TMP=$(mktemp -d)
trap 'kill $SRV_PID 2>/dev/null || true; rm -rf "$TMP"' EXIT

make all
[ -x bin/phone-home-agent ] || { echo "SMOKE FAIL: phone-home-agent not built" >&2; exit 1; }

# --simulate: deploy uses the fake executor (no real IPMI) so smoke is hermetic.
./bin/cube-cos-snapshot --port "$PORT" --data-dir "$TMP/data" --export-dir "$TMP/export" --simulate &
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

# Assignment: bind the machine to a cluster node (1:1), then unassign.
curl -sf -X PUT -H 'Content-Type: application/json' \
  -d '{"clusterId":"cl-smoke","hostname":"cube-1","osDisk":"sda"}' \
  "http://localhost:$PORT/api/v1/machines/$MID/assignment" | grep -q '"hostname":"cube-1"' || fail "assign"
curl -sf "http://localhost:$PORT/api/v1/machines/$MID" | grep -q '"osDisk":"sda"' || fail "assignment osDisk"
curl -sf -X DELETE "http://localhost:$PORT/api/v1/machines/$MID/assignment" >/dev/null || fail "unassign"

# Import from CSV (multipart).
printf 'label,bmc_address,bmc_username,bmc_password\nimp-1,10.0.0.21,admin,pw\nimp-2,10.0.0.22,admin,pw\n' > "$TMP/machines.csv"
IMP=$(curl -sf -F "file=@$TMP/machines.csv;type=text/csv" "http://localhost:$PORT/api/v1/machines/import")
echo "$IMP" | grep -q '"created":2' || fail "csv import ($IMP)"

# Deploy orchestration (simulated): assign all ha3 nodes to machines, plan,
# deploy, and drive an agent check-in + report to reach applied.
PUT_MID() {
  curl -sf -X POST -H 'Content-Type: application/json' \
    -d "{\"label\":\"$1\",\"bmc\":{\"address\":\"10.9.9.$2\",\"username\":\"admin\",\"password\":\"x\"}}" \
    "http://localhost:$PORT/api/v1/machines" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'
}
i=1
for host in cube-1 cube-2 cube-3; do
  DID=$(PUT_MID "$host" "$i")
  # Give the machine a MAC so agent check-in can match it.
  curl -sf -X PUT -H 'Content-Type: application/json' \
    -d "{\"clusterId\":\"$ID\",\"hostname\":\"$host\",\"osDisk\":\"sda\"}" \
    "http://localhost:$PORT/api/v1/machines/$DID/assignment" >/dev/null || fail "assign $host"
  i=$((i+1))
done

# Re-create the cluster (it was deleted above) and re-assign is simpler: just
# re-save ha3 so the plan can resolve node hostnames.
curl -sf -X PUT -H 'Content-Type: application/json' \
  --data @internal/model/testdata/ha3.json \
  "http://localhost:$PORT/api/v1/clusters/$ID" >/dev/null || fail "re-save cluster"

curl -sf "http://localhost:$PORT/api/v1/clusters/$ID/deploy/plan" | grep -q '"allAssigned":true' || fail "deploy plan not all-assigned"
curl -sf -X POST -H 'Content-Type: application/json' -d '{"confirm":true}' \
  "http://localhost:$PORT/api/v1/clusters/$ID/deploy" >/dev/null || fail "deploy start"

# Agent check-in/report drives cube-1 to done.
curl -sf -X POST -H 'Content-Type: application/json' \
  -d '{"clusterId":"'"$ID"'","hostname":"cube-1","state":"done","message":"applied"}' \
  "http://localhost:$PORT/api/v1/agents/report" >/dev/null || fail "agent report"
for _ in $(seq 1 20); do
  curl -sf "http://localhost:$PORT/api/v1/clusters/$ID/deploy" | grep -q '"cube-1":{[^}]*"state":"done"' && break
  sleep 0.2
done
curl -sf "http://localhost:$PORT/api/v1/clusters/$ID/deploy" | grep -q '"state":"done"' || fail "deploy node not done"

curl -sf -X DELETE "http://localhost:$PORT/api/v1/machines/$MID" || fail "delete machine"

echo "SMOKE PASS"
