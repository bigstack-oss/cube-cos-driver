# REST API (v1)

Base path: `/api/v1`. Errors return JSON `{"message": "..."}` with an
appropriate status code. `{id}` is the cluster **short id** — the last 12
characters of the cluster UUID.

## `GET /api/v1/clusters`

List stored clusters.

```json
[
  { "id": "aabbccddee01", "name": "sky-lab", "nodes": ["cube-1", "cube-2", "cube-3"] }
]
```

## `PUT /api/v1/clusters/{id}`

Save a cluster and (re)generate all snapshots. Body is a `clusterDetail`
document (same schema as the legacy tool; see the fixtures under
`internal/generator/testdata/fixtures/`). Requirements:

- `{id}` must equal the last 12 chars of `clusterInfo.id`.
- The detail must pass validation (unique hostnames, resolvable role
  interfaces, HA settings when `HA: true`, …) — otherwise `400`.

Generation is atomic: on failure the previously stored version is untouched.
Returns `200 {"message": "saved"}`.

```bash
curl -X PUT -H 'Content-Type: application/json' \
  --data @clusterDetail.json http://localhost:3001/api/v1/clusters/aabbccddee01
```

## `GET /api/v1/clusters/{id}`

Full stored `clusterDetail` JSON. `404` if unknown.

## `DELETE /api/v1/clusters/{id}`

Remove the cluster: data dir, cluster zip, and exported flat snapshots.
Returns `204`.

## `GET /api/v1/clusters/{id}/download`

The whole-cluster zip (`<clusterName>.zip` via Content-Disposition),
containing `<id>/clusterDetail.json` and `<id>/<hostname>.snapshot` per node.

## `GET /api/v1/clusters/{id}/nodes/{hostname}/download`

A single node's `.snapshot` file.

## Machines (hardware inventory)

A global pool of machines keyed by their BMC. Passwords are stored encrypted
and never returned; responses carry `hasPassword` instead. Used to fetch
hardware facts and (later) drive zero-touch install.

### `GET /api/v1/machines`

List machines (no secrets), each with its last-fetched `inventory` and
`fetchState` (`idle` | `fetching` | `ok` | `error`).

### `POST /api/v1/machines`

Create. Body: `{"label": "...", "bmc": {"address": "...", "username": "...", "password": "..."}}`.
`address` may be `host` or `host:port`. `label` and `address` are required.

### `GET /api/v1/machines/{id}` · `PUT /api/v1/machines/{id}` · `DELETE /api/v1/machines/{id}`

Get / update / delete. On `PUT`, omit `bmc.password` (or send `null`) to keep
the stored password; send `""` to clear it.

### `POST /api/v1/machines/{id}/fetch`

Trigger asynchronous hardware discovery over the BMC (Redfish first, IPMI FRU
fallback). Returns `202`; progress is visible via `fetchState` on subsequent
`GET`s. A second fetch while one is in flight is a no-op `202`.

Fetched `inventory` includes source, serial/manufacturer/model, CPU
model/count/cores, memory, and lists of NICs (name/MAC/speed), disks
(model/size/type), and PCIe cards.

### `POST /api/v1/machines/import`

Multipart upload (`file` field) of an `.xlsx` or `.csv` with header columns
`label, bmc_address, bmc_username, bmc_password`. Bulk-creates machines and
returns `{"created": N, "errors": [{"row": R, "message": "..."}]}`. A
downloadable template is at `GET /api/v1/machines/import/template`.

### `PUT /api/v1/machines/{id}/assignment`

Bind a machine to a cluster node. Body `{"clusterId": "...", "hostname": "...", "osDisk": "..."}`.
Enforces 1:1: any other machine on the same node is unbound. `DELETE` on the
same path clears the binding. Bindings for a cluster are also cleared when the
cluster is deleted.

## Deploy orchestration

Drive a fully-assigned cluster's nodes through PXE imaging (over IPMI) and the
phone-home agent's snapshot apply. All nodes must be assigned first.

### `GET /api/v1/clusters/{id}/deploy/plan`

Dry-run: `{"allAssigned": bool, "nodes": [{hostname, assigned, machineLabel, bmcAddress, osDisk, macs}]}`.
Returns `409` (with the same body) when not every node is assigned.

### `POST /api/v1/clusters/{id}/deploy`

Body `{"confirm": true, "hostnames": [...optional subset...]}`. Requires
`confirm`; `409` if any node is unassigned. Starts the deploy and returns the
initial status (`202`).

### `GET /api/v1/clusters/{id}/deploy`

Current status: `{clusterId, startedAt, nodes: {<hostname>: {state, message, preflight[]}}}`.
States: `pending → bmc-preflight → set-boot-pxe → power-cycle → netbooting →
imaging → imaged → checked-in → net-preflight → applying → applied → done`
(or `error`).

### `POST /api/v1/clusters/{id}/deploy/cancel`

Stops stepping (does not force-power-off a running install).

### `POST /api/v1/agents/checkin`

Called by `phone-home-agent`. Body `{macs, serial}`; the server matches MACs to
an appointment and returns `{appointed, clusterId, hostname, snapshotUrl,
serverTimeUTC, preflight:{gateway, dns[], server, peers[]}}`, advancing that
node to `checked-in`.

### `POST /api/v1/agents/report`

Agent progress: `{clusterId, hostname, state, message, preflight[]}` — advances
the node's deploy state (net-preflight results, applying/applied/error).

## `GET /healthz`

Liveness probe; returns `200 ok`.

## Node-side pull (pxeserver mode)

Not part of this API: with `--export-dir /var/ftpboot`, flat
`<hostname>.snapshot` files are served by the pxeserver's lighttpd at
`http://<pxeserver-ip>/<hostname>.snapshot`, which is what the CubeCOS CLI's
`snapshot pull url <hostname>` fetches by default.
