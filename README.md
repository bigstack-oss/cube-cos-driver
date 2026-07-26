# cube-cos-driver

Cube Snapshot Generator v2 — a web tool that generates **CubeCOS cluster
snapshots**: per-node configuration bundles (`<hostname>.snapshot`) that a
freshly imaged node applies in one step (`snapshot pull` + `snapshot apply`)
instead of walking through first-time setup.

The whole app ships as **one static Go binary** with the React SPA (built on
[`cube-cos-ui`](https://github.com/bigstack-oss/cube-cos-ui)'s
`@cube-frontend/ui-library`) embedded. No runtime dependencies — air-gap
friendly, and small enough to embed in the CubeCOS **pxeserver** image.

This replaces the legacy
[`cube-snapshot-generator`](https://github.com/bigstack-oss/cube-snapshot-generator)
(React + Carbon + Express). The generated YAML is byte-identical (golden
tests capture the legacy output), and `clusterDetail.json` exports from the
old tool import cleanly.

## Build

Requirements (build-time only): Go ≥ 1.24, Node ≥ 24.15, pnpm ≥ 10.33.

```bash
git clone --recurse-submodules https://github.com/bigstack-oss/cube-cos-driver.git
cd cube-cos-driver

# Node 24 + pnpm via nvm/corepack if needed:
#   nvm install 24 && corepack enable

make all        # build SPA → embed → bin/cube-cos-driver
make test       # go vet + go tests
bash scripts/smoke.sh   # full end-to-end check
```

## Run

### Team service

```bash
./bin/cube-cos-driver --port 3001 --data-dir /var/lib/cube-cos-driver
```

Open `http://<host>:3001/`. Clusters saved in the UI are generated and
stored under `--data-dir`.

### Deploy orchestration

When every node in a cluster has a server assigned, **Deploy to cluster** drives
each node's BMC over IPMI (power-cycle into one-time PXE boot), tracks imaging,
then the `phone-home-agent` on the freshly-imaged node checks in, syncs its
clock, runs a network preflight, and applies its appointed snapshot — reporting
progress live. Deploy is plan-first and confirm-gated (it powers real servers).

Two binaries are built (`make all`): `bin/cube-cos-driver` (server) and
`bin/phone-home-agent` (ships in the CubeCOS image). For demos without
hardware, run the server with `--simulate` to use a fake executor.

### Hardware inventory

The **Hardware** page (left nav) maintains a global pool of machines keyed by
their BMC (IPMI/Redfish). Add a machine with its BMC address / username /
password, then **Fetch** to read hardware facts (CPU, memory, NICs, disks,
PCIe cards, serial) — Redfish first, IPMI FRU fallback. This is the
foundation for the zero-touch install roadmap (`docs/roadmap.md`): registered
hardware is later appointed to cluster nodes and driven over IPMI.

BMC passwords are **encrypted at rest** (AES-256-GCM). The key is resolved
from `SNAPSHOT_SECRET_KEY` (env) or `--secret-key-file`; if neither is set, a
key file is auto-generated at `<data-dir>/.secret-key` (0600). API responses
never return the password — only a `hasPassword` flag. Note the UI itself has
no authentication yet, so run it on a trusted/isolated management network.

### pxeserver mode

```bash
./bin/cube-cos-driver --port 3001 \
  --data-dir /var/lib/cube-cos-driver \
  --export-dir /var/ftpboot
```

`--export-dir` mirrors every generated `<hostname>.snapshot` flat into the
pxeserver web root (lighttpd serves `/var/ftpboot` on `:80`), so on a node:

```text
# hex_cli > snapshot > pull > url
snapshot pull url <hostname>     # expands to http://192.168.1.150/<hostname>.snapshot
snapshot apply
```

Flags can also be set via `PORT`, `DATA_DIR`, `EXPORT_DIR` env vars.

## Development

```bash
go run ./cmd/cube-cos-driver                # API on :3001
pnpm install && pnpm -C web dev               # Vite dev server, proxies /api → :3001
pnpm -C web test && pnpm -C web tsc           # web tests + typecheck
```

## Repo layout

- `cmd/cube-cos-driver`, `internal/` — Go server: REST API
  ([docs/api.md](docs/api.md)), snapshot generator (byte-parity with legacy;
  see `internal/generator/testdata/golden`), on-disk store, embedded SPA.
- `web/` — the SPA (React 19 + TS + Vite + Tailwind + `@cube-frontend/ui-library`).
- `external/cube-cos-ui` — git submodule providing the UI library/theme
  (consumed as source via the pnpm workspace).

### Bumping the cube-cos-ui submodule

```bash
git -C external/cube-cos-ui pull origin develop
# IMPORTANT: re-sync the catalog: block in pnpm-workspace.yaml with
# external/cube-cos-ui/pnpm-workspace.yaml (pnpm only reads the root file).
pnpm install && pnpm -C web build
git add external/cube-cos-ui pnpm-workspace.yaml pnpm-lock.yaml
git commit -s -m "chore: bump cube-cos-ui"
```

## Storage layout

```
<data-dir>/<clusterShortId>/clusterDetail.json   # saved cluster definition
<data-dir>/<clusterShortId>/<hostname>.snapshot  # per-node zip
<data-dir>/<clusterShortId>.zip                  # whole-cluster bundle
<export-dir>/<hostname>.snapshot                 # flat copies (pxe mode)
```

A `.snapshot` is a zip of `etc/policies/{cubesys,network,time}/*.yml`,
`etc/appliance/state/{sla_accepted,configured}`, and a `Comment` file —
exactly what `hex_config snapshot_apply` expects.
