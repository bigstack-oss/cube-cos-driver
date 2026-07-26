# cube-cos-driver (Cube Snapshot Generator v2) — Design

Date: 2026-07-24
Status: Approved (Travis, 2026-07-24)

## Goal

Rewrite the Cube Snapshot Generator as a React 19 + TypeScript SPA built on
`@cube-frontend/ui-library` (COS look & feel), served by a **single static Go
binary** that also generates and stores snapshots. One artifact, zero runtime
dependencies, air-gap-safe.

Two deployment targets:

1. **Team service** — run the binary anywhere; the team generates snapshots
   through the web UI.
2. **Embedded in the cubecos pxeserver image** — the same binary runs inside
   the pxeserver ramdisk. Users bring up the pxeserver, open the UI, generate
   snapshots for the cluster, PXE re-image nodes, then on each node run
   `snapshot pull url <hostname>` followed by `snapshot apply` to skip
   step-by-step first-time configuration.

## Background facts (verified in cubecos)

- `snapshot pull url <name>` on a node defaults to
  `http://192.168.1.150/<name>.snapshot` (`hex/src/modules/cli_snapshot.cpp`).
  `192.168.1.150` is `PXESERVER_IP`, and the pxeserver's lighttpd serves
  `/var/ftpboot` at the web root on `:80`
  (`hex/src/hex_pxe_server_rd/Makefile`).
- The pxeserver ramdisk is minimal (lighttpd, dnsmasq, dhcpd, sshd; no
  Node.js), aggressively stripped. A static Go binary fits; a Node runtime
  does not.
- A `.snapshot` is a zip containing `etc/policies/cubesys/cubesys1_0.yml`,
  `etc/policies/network/network1_0.yml`, `etc/policies/time/time1_0.yml`,
  `etc/appliance/state/{sla_accepted,configured}`, and a `Comment` file.
  `snapshot_get_comment` validates it before apply.
- `@cube-frontend/ui-library` is private and source-form inside the
  cube-cos-ui pnpm monorepo (React 19, Vite, Tailwind, `workspace:*` +
  `catalog:` deps) — consumable only from within the monorepo workspace
  context, hence the git submodule.

## Decisions (made with Travis)

| Decision | Choice |
| --- | --- |
| Backend / artifact | Go single binary, SPA embedded via `go:embed` |
| Code location | New repo `bigstack-oss/cube-cos-driver` (this repo); `cube-cos-ui` as git submodule; legacy `cube-snapshot-generator` left untouched (archive later) |
| Scope | Feature parity minus the Swagger UI page (+ its basic-auth) |
| API compatibility | Not required — clean redesign; `clusterDetail` JSON schema kept unchanged |

## Repo layout

```
cube-cos-driver/
├── cmd/cube-cos-driver/main.go
├── internal/
│   ├── api/          # REST handlers, SPA serving
│   ├── generator/    # YAML render + zip assembly; template embedded (go:embed)
│   └── storage/      # cluster dirs, clusterDetail.json, .snapshot/.zip files
├── web/              # SPA package (React 19 + TS + Vite + Tailwind)
├── external/cube-cos-ui/   # git submodule
├── pnpm-workspace.yaml     # packages: web + external/cube-cos-ui/packages/*
└── Makefile          # pnpm build → copy dist into embed dir → go build
```

The legacy implementation stays in `bigstack-oss/cube-snapshot-generator`
(reference for golden tests; archived once this repo reaches parity). The
snapshot template files are copied from it into `internal/generator/`.

## Backend (Go, stdlib `net/http`)

Flags (env-overridable):

- `--port` — default `3001`.
- `--data-dir` — default `./storage/snapshot`. Cluster store:
  `<data-dir>/<clusterShortId>/` containing `clusterDetail.json`,
  `<hostname>.snapshot` per node, and `<clusterShortId>.zip`.
- `--export-dir` — optional. When set (pxeserver: `/var/ftpboot`), every
  generated `<hostname>.snapshot` is *also* copied flat into this directory
  so nodes can `snapshot pull url <hostname>` with the default URL. On
  cluster regeneration/delete, stale exported files for that cluster's nodes
  are replaced/removed.

### REST API (v1)

| Method & path | Purpose |
| --- | --- |
| `GET /api/v1/clusters` | List digests: `{id, name, nodes[]}` per stored cluster |
| `GET /api/v1/clusters/{id}` | Full `clusterDetail` JSON |
| `PUT /api/v1/clusters/{id}` | Body = `clusterDetail`; validates, generates all node snapshots + cluster zip atomically (temp dir + rename), persists |
| `DELETE /api/v1/clusters/{id}` | Remove cluster dir, zip, exported snapshots |
| `GET /api/v1/clusters/{id}/download` | Cluster zip (`<name>.zip` download name) |
| `GET /api/v1/clusters/{id}/nodes/{hostname}/download` | Single `.snapshot` |
| `GET /healthz` | Liveness |

`{id}` is the 12-char short id (last 12 of the client UUID), as today.
Everything else (`/`, `/assets/...`) serves the embedded SPA with an
`index.html` fallback. The API is documented in `docs/api.md` (no served
Swagger UI).

### Generator

Port of `server/routers/getYaml.js` + `createNodeSnapshot.js`:

- Renders `cubesys1_0.yml`, `network1_0.yml`, `time1_0.yml` with the same
  role/option matrix (`roleOptions`), HA handling (`controller`,
  `control-vip`, `control-hosts`, `control-addrs`), interface typing
  (init/bond/vlan → 0/1/2/3), and null→empty YAML style.
- Template files (`storage/snapshot_template`) embedded via `go:embed`; no
  filesystem template or shell scripts at runtime; zips built with
  `archive/zip`.
- **Golden-file tests**: representative `clusterDetail.json` fixtures
  (non-HA control-converged, HA 3-node, compute/storage mix, bonds+VLANs)
  with expected YAML outputs captured from the legacy JS implementation.
  The Go output must match byte-for-byte (modulo the timestamped `Comment`).

### Error handling

JSON error bodies `{message}` with proper status codes (400 validation, 404
missing cluster/node, 500 generation failure). Generation is all-or-nothing
per PUT: failures leave the previous stored state untouched.

## Frontend (web/)

Structure mirrors `cube-frontend-web-app` idioms (Vite, `@vitejs/plugin-react-swc`,
Tailwind + `@cube-frontend/ui-theme`, dev proxy `/api` → `:3001`).

Pages and flows (parity with the old app):

- **Landing page** — intro/how-to text; cluster cards showing name and node
  role breakdown; actions: create (wizard), import `clusterDetail.json`
  (schema-validated), rename, delete; open a cluster.
- **Cluster wizard** — steps: License agreement → Name → DNS → Timezone →
  Role settings (external IP, region, secret seed, mgmt CIDR) → HA toggle
  (virtual IP + virtual hostname). `CosModal` + `CosStepProcess`.
- **Node wizard** — steps: Hostname → Network (init/bond/VLAN interface
  editor, enable/IP/mask, default interface + gateway) → Role
  (control/compute/storage/control-converged/edge-core/moderator) + role
  interface mapping (mgmt/provider/overlay/storage/storage-backend).
- **Cluster page** — cluster detail card, node table (`CosBasicTable`) with
  edit/duplicate/delete, validation notifications, Save-to-server
  (PUT), Download cluster zip / per-node snapshot, snapshot-URL modal
  (shows `snapshot pull url` command per node).

Component mapping: Carbon `TextInput/Dropdown/Toggle/RadioButton/DataTable/
Modal/Notification/Header` → `CosInput/CosDropdown/CosToggle/CosRadioButton/
CosBasicTable/CosModal/CosNotification|CosMessage/CosHeader`.

State model unchanged: drafts in `localStorage`
(`<id>-nodes`, `<id>-cluster`, `clustersInfo`), explicit Save pushes to the
server; on open, server copy (if any) hydrates missing local state. The
`clusterDetail` JSON schema is unchanged so previously exported files import
cleanly. Validation rules (name sanitization, IP/CIDR checks, role/IF
completeness — `isDanger` equivalents) are ported to TS.

## PXE embedding (follow-up, cubecos repo)

Out of scope for this repo but designed for: a cubecos PR adds the built
binary + systemd unit to `hex_pxe_server_rd` (UI on `:3001`,
`--export-dir /var/ftpboot`), keeping lighttpd on `:80` untouched. Air-gap
compliant: single amd64 artifact, no runtime downloads. The legacy
`PXE_MODE`/`getList` toggle disappears — the list API is always available.

## Testing & CI

- Go: `go vet`, unit tests, golden-file generator tests.
- Web: `tsc --noEmit`, eslint, vitest for validation/model logic.
- GitHub Actions: build web + binary, run all checks, upload the binary as
  an artifact. (Legacy Jenkins/bitbucket pipelines removed.)

## Risks

- **YAML fidelity** — mitigated by golden tests against legacy output.
- **Submodule drift** — cube-cos-ui pinned; bumped deliberately.
- **Build toolchain** — Node ≥24 + pnpm + Go required at build time only;
  runtime needs nothing.
