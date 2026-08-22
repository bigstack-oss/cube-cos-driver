# Enterprise Modules Installer — Design

**Status:** approved (design), pending implementation plan
**Date:** 2026-08-07
**Repo:** cube-cos-driver
**Source of truth for install procedure:** Notion "CMP 2.1.0 and 2.0.0 Installation" (App-FW 2.0 + CMP path)

> **Historical record.** This design predates the `advisor` module added 2026-08-23 (see `docs/superpowers/plans/2026-08-23-advisor-enterprise-module.md`). The two-module (`appfw`/`cmp`) scope below is left as originally written; it is no longer the full module set.

## Goal

Extend cube-cos-driver to own the CubeCOS **Day-1** enterprise-module installs — **App-Framework 2** and **Cube-CMP 2.1.0** — against an already-configured, running cluster reached via its VIP. Today the driver is a provisioning/imaging tool that never talks to a live cluster; this feature introduces the driver's first live-cluster client.

## Global constraints

- **Air-gapped install is mandatory** (Cube product rule). All artifacts are pre-staged offline; nothing is fetched from the internet at install time. Every install additionally offers an **air-gap enforcement** toggle that blocks the *cluster's* public egress during the install to prove the path is air-gap-clean.
- **No cluster-side changes required.** The driver drives the documented, guard-gated `hex_cli` path over SSH; it does not add or depend on new `cube-cos-api` endpoints.
- **Single supported entrypoint per action.** Use `hex_cli` (setuid, FTS/commit-guarded), not raw `appctl`/`hex_sdk`, so we inherit its guards and match the documented path.
- Keep code comments short; rationale lives here / in the issue+PR, not inline.

## Background: what the install actually is

`hex_cli` is a thin wrapper delegating to node-local tools (traced in cubecos `core/modules/cli_app.cpp`, `cli_iaas_image.cpp`):

- `iaas image import_fs|import_lb|import` → `hex_sdk os_image_import*` — reads the image from the cluster's cephfs glance dir (or USB) and creates it in glance.
- `app framework_create <name> <public-net> <mgmt-net> <lb-ip> <os-image>` → `appctl create framework …` — the `cube-cos-appctl` binary stands up the Octavia LB + the RKE2 cluster VM from the rancher image.
- `app app_register <pigz> <framework> [skip_flavor]` → `hex_sdk app_import` — extracts the `.pigz` and runs its bundled `import.sh` with `ENV_PROJ_NAME=<framework>` to register the `cube-apps` chart repo / CMP app.

Support matrix (relevant row): CubeCOS 3.1.x → App-FW **2.0** → CMP 2.0.0 or 2.1.0.

Artifacts required (offline): `rancher-cluster-image-rke2-v1.32.4.raw` (~40 GiB), `amphora-x64-haproxy-yoga.qcow2` (Octavia LB), `manila-service-image-yoga.qcow2` (NFS), and the CMP bundle `cube-portal-<ver>+rev*.pigz`.

## Architecture

### Model — step-sequence install job
An **Install** is one job per `(clusterID, module)`, targeting **one control node via the VIP**, executed as an **ordered list of steps**. This fits the single-endpoint, linear reality — unlike the cluster deploy's per-node goroutine state machine, which is deliberately *not* mirrored. Auto mode runs all steps; manual mode gates each step behind a **Next** button (reusing the deploy's `ManualStep`-cursor concept). Each step is one SSH command whose stdout streams to the UI.

### Module scope (set by the user's choice)
- **Install App-Framework** → ends when the framework is created.
- **Install Cube-CMP** → ensures a framework exists (detect via `app framework_list`; if none, run the App-Framework sequence first), then `app_register`. Ends with App-FW + CMP. "Set administrator permission" is surfaced as a next-step card (see §"Completion"), not automated in v1.

### New live-cluster client — `internal/clusterssh`
A new package behind a **mockable interface** (mirrors the deploy manager's `Executor`/`GateWriter`/`Verifier` seams so the orchestration is unit-testable without a real cluster):

```go
type Client interface {
    Run(ctx context.Context, cmd string, onLine func(string)) error // stream stdout
    Push(ctx context.Context, localPath, remotePath string) error   // scp
}
```

- Transport: `golang.org/x/crypto/ssh` + scp. Target `root@<VIP>`.
- VIP source: the cluster's `HASettings.VirtualIP` (already rendered in `internal/generator/render.go`).
- Credentials: default password `Cube@<last-two-octets-of-VIP>`, overridable per-install in the modal; stored encrypted via the existing `secret` package + `.secret-key` (as machine BMC passwords are).

### State & store — `internal/enterprise`
A `Manager` + `Install`/`Step` model + JSON disk store, mirroring `internal/orchestrator`'s `Manager`/`store.go`:

```go
type StepState string // pending | active | done | error | skipped
type Step struct { Name, Title string; State StepState; Output string; Err string }
type Install struct {
    ClusterID string; Module string   // "appfw" | "cmp"
    StartedAt string; Manual bool; ManualStep int
    SimulateAirgap bool
    Params InstallParams              // framework_create inputs + chosen artifacts
    Steps []*Step; Current int
    State string                      // running | done | error
    Portal string                    // http://<lb-ip> for the completion card
}
```

Persisted at `<DATA_DIR>/installs/<clusterID>-<module>.json`.

### Data-dir layout (pre-staged, driver-discovered)
```
<DATA_DIR>/enterprise/
  appfw/    rancher-cluster-image-rke2-*.raw, amphora-*.qcow2, manila-*.qcow2
  cubecmp/  cube-portal-*.pigz
<DATA_DIR>/installs/  <clusterID>-<module>.json
```
Operators drop artifacts into `appfw/` and `cubecmp/` via offline media/scp; the driver scans and lists them (same pattern as the PXE image picker and the file-on-disk packed agent). No browser upload (the rancher image alone is ~40 GiB). New folders are wired in `internal/api/server.go` alongside `machines/`/`deploys/`.

## Step sequences

**App-Framework** (`module=appfw`):
1. `preflight` — VIP reachable + creds valid; required `appfw/` artifacts present; no framework of the requested name already exists (`app framework_list`).
2. *(if air-gap toggle)* `airgap-apply` — `cubectl exec -p 'hex_sdk airgap_sim_apply'`.
3. `import_fs` — scp `manila-*.qcow2` → cephfs glance dir; `hex_cli -c iaas -c image -c import_fs local <file>`.
4. `import_lb` — scp `amphora-*.qcow2`; `import_lb local <file>`.
5. `import` — scp `rancher-*.raw`; `iaas image import local <file>`.
6. `framework_create` — `hex_cli -c app -c framework_create <name> <public-net> <mgmt-net> <lb-ip> <os-image>`.

**Cube-CMP** (`module=cmp`):
- If no framework exists → steps 1–6 above first (auto-install App-Framework).
- `app_register` — scp `cube-portal-<ver>.pigz` → `/mnt/cephfs/update`; `hex_cli -c app -c app_register <path> <framework> skip_flavor`.
- `complete` — emit the next-steps card.

Image-import idempotency: skip the import if a glance image of that name already exists.
A failed step stops the job (`error`, retryable); manual mode does not auto-advance past a failed step.

## `framework_create` inputs (modal form)
- `project name` — operator.
- `public net` — default `public`.
- `mgmt net` — default `public`.
- `LB IP` — operator; must be within the public-net range.
- `OS image` — auto-selected from the staged rancher `.raw` in `appfw/`.
- Cube-CMP adds: a **framework picker** (from `app framework_list`) and a **`.pigz` picker** (from `cubecmp/`).

## Air-gap enforcement toggle
Advanced option on every install. When set, `airgap-apply` runs before the install steps (`cubectl exec -p 'hex_sdk airgap_sim_apply'`, cluster-wide) and the block is **left in place** afterward for inspection; the operator lifts it with `cubectl exec -p 'hex_sdk airgap_sim_clear'`. Reuses the `airgap_sim_apply`/`airgap_sim_clear` functions (cubecos `sdk_network.sh`) validated on the sky cluster on 2026-08-07.

## Completion — next-steps card
On success the install-complete view shows a **Next steps** card:
- `✅ <module> installed on <cluster>` (framework `<proj>`, cube-portal `<ver>` for CMP).
- The clickable CMP portal link `http://<lb-ip>` (the LB IP entered at `framework_create`), opening in a new tab.
- Instruction: log into the portal as local admin and grant `admin` the administrator permission (first-time). Link to the runbook.
- An **automation hook** (no-op in v1): the completion step is structured so an automated grant — `kubectl patch token …` (Rancher) + a Keycloak `ProjectRole=USER_DEFINED_DEFAULT-admin` attribute on the admin user — can be slotted in later without reshaping the flow. **Open item:** verify on a real 2.1.0 install whether `app_register` already sets the keycloak attribute; if so, this collapses to "verify."

## REST surface (new `enterpriseHandlers`, registered in `internal/api/server.go` next to `deployHandlers`)
- `GET  /api/v1/enterprise/artifacts` — discovered `appfw/` + `cubecmp/` files.
- `POST /api/v1/clusters/{id}/enterprise/install` — start `{module, params, manual, simulateAirgap, password}`.
- `GET  /api/v1/clusters/{id}/enterprise/install` — status (steps + streamed output + current cursor).
- `POST /api/v1/clusters/{id}/enterprise/install/step/next` — manual Next.
- `POST /api/v1/clusters/{id}/enterprise/install/cancel` — cancel.
- Cluster dropdown reuses the existing `GET /api/v1/clusters`.

## UI (mirrors the adjacent deploy modal/progress)
- Nav: add **Enterprise Modules** to `web/src/components/AppSidebar.tsx` `navItems` and a `<Route>` in `web/src/App.tsx`.
- `web/src/pages/enterprise/EnterprisePage.tsx` — lists the two modules as cards, each with **Install** (extensible for future modules).
- **InstallModal** — cluster dropdown; password field (default `Cube@<last-two-octets>` shown); `framework_create` params; auto/manual toggle; **Advanced** (collapsed) → air-gap toggle + artifact pickers; Cube-CMP adds framework + `.pigz` pickers.
- **InstallProgress** — step list with per-step streaming output; manual **Next**; on success the completion **Next-steps card**.
- API client `web/src/api/enterprise.ts` mirrors the Go types (as `deploy.ts` does).

## Error handling
Preflight fails fast and explains: unreachable VIP, bad credentials, missing artifact, existing framework (App-Framework), glance image already present (skip, not fail). Step failure → job `error` with the captured stderr; retryable from the failed step.

## Testing
Unit tests drive a **mock `clusterssh.Client`** to verify: step ordering per module; auto vs manual gating (Next advances exactly one step; no auto-advance past failure); the Cube-CMP "no framework → run App-Framework first" branch; air-gap `airgap-apply` ordered before the install steps; idempotent image-import skip. No real cluster required in CI.

## Extension points (existing code to hook)
- Nav/route: `web/src/components/AppSidebar.tsx` (`navItems`), `web/src/App.tsx` (`<Routes>`).
- Handler assembly: `internal/api/server.go` (`newHandler`) — instantiate `enterpriseHandlers` + wire the new data-dir folders.
- Orchestration/store pattern to mirror: `internal/orchestrator/manager.go`, `model.go`, `store.go`.
- Mockable-seam pattern: the deploy manager's `Executor`/`GateWriter`/`Verifier` interfaces.
- Offline artifact serving pattern: `deploy.go` `agentBinary` + `--agent-binary` flag plumbing (`cmd/cube-cos-driver/main.go`, `server.go`).
- Credential encryption: the `secret` package + `.secret-key`.

## Defaults chosen (approved)
- One **Enterprise Modules** page listing both modules (not two separate nav items) — for future extensibility.
- Image-import idempotency = skip if a glance image of that name already exists.

## Out of scope (v1)
- Automating "set administrator permission" (Rancher token + Keycloak attribute) — surfaced as a next-step card with an automation hook; revisit after verifying real-install behavior.
- Automating the Rancher `cube-portal` chart deploy beyond what `app_register` performs.
- Upgrade flows (the doc's Upgrade section) — install only for v1.
- App-FW 1.0 / `framework_install` path (legacy; CubeCOS 3.0.0 only).
