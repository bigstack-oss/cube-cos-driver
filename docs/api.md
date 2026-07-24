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

## `GET /healthz`

Liveness probe; returns `200 ok`.

## Node-side pull (pxeserver mode)

Not part of this API: with `--export-dir /var/ftpboot`, flat
`<hostname>.snapshot` files are served by the pxeserver's lighttpd at
`http://<pxeserver-ip>/<hostname>.snapshot`, which is what the CubeCOS CLI's
`snapshot pull url <hostname>` fetches by default.
