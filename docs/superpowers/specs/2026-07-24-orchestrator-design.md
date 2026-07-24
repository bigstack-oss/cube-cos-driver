# Deploy Orchestrator — Design

Date: 2026-07-24
Status: Proposed (awaiting Travis approval)

## Goal

Once a cluster's nodes are fully associated (each node bound to a BMC machine
with an OS disk and network binding), the user can **deploy** the cluster: the
orchestrator drives each node's BMC over IPMI to power-cycle it into a
one-time PXE netboot, tracks install progress, and — after the node reboots
into freshly-imaged CubeCOS — the **phone-home agent** running on that node
checks in, pulls its appointed snapshot, applies it, and reports back. Full
hands-free path.

## Two binaries

This repo now builds **two** static Go binaries:

1. `cmd/cube-cos-snapshot` — the existing server (SPA + snapshot generator +
   inventory + orchestrator API), runs on the pxeserver.
2. `cmd/cube-cos-agent` — a tiny agent that ships inside the CubeCOS image and
   runs on first boot of an **unconfigured** node. It phones home to the
   snapshot server, learns which snapshot it's been appointed, applies it, and
   self-disables once configured. (A cubecos packaging change installs the
   systemd unit that launches it; the agent *code* lives here.)

Safety: deploy always shows a **dry-run plan** and requires explicit
confirmation; nodes can be deployed individually; nothing is powered without a
BMC-reachability **preflight**.

## Decisions (approved)

| Decision | Choice |
| --- | --- |
| Scope this iteration | Job engine + **real IPMI** power/bootdev; **phone-home agent binary** in this repo; agent-facing API is real. |
| Node consume | `cube-cos-agent` binary (built here) phones home, pulls + applies its snapshot, reports back. |
| Safety | Plan → explicit confirm → per-node; preflight before power. |
| Progress transport | Polling (consistent with the app's existing polling). |
| Placement | Same Go binary, `internal/orchestrator`; runs on the pxeserver. |

## Preconditions

Deploy is available for a cluster only when **every node has an assignment**
(machine + OS disk). The orchestrator reads, per node:
- BMC address/username/password (decrypted from the machine record),
- the assigned machine's NIC MACs (from discovery) — used to recognize the
  node's DHCP lease during netboot,
- OS disk + hostname (for the plan and future apply).

## Per-node state machine (this iteration)

```
pending → preflight → set-boot-pxe → power-cycle → netbooting → imaging → imaged
       → checked-in → applying → applied → done
   any step → error (with message); node is independently retryable
```
The stages from `checked-in` onward are driven by the agent's check-in/report
calls (correlated to the node by MAC), not by the orchestrator polling.

- **preflight**: IPMI Get Chassis/Power Status succeeds (BMC reachable, creds
  valid).
- **set-boot-pxe**: IPMI chassis bootdev = PXE, one-time.
- **power-cycle**: power on if off, else power cycle.
- **netbooting**: a DHCP lease for one of the node's known MACs appears
  (real: read the pxeserver's `dnsmasq.leases`; fake: simulated).
- **imaging**: the node is fetching its `.pkg` (real: pxeserver lighttpd
  access log; fake: simulated). 
- **imaged**: install media fully fetched / node reboots off-media.
- **checked-in**: the agent on the freshly-imaged node POSTed to
  `/api/v1/agents/checkin` with matching MACs.
- **applying → applied → done**: the agent pulled its appointed snapshot, ran
  apply, and reported success (via `/api/v1/agents/report`).

## Executor interface (the CI/hardware seam)

```go
type Node struct {
    Hostname string
    BMC      struct{ Address, Username, Password string }
    MACs     []string // candidate NIC MACs from discovery
    OSDisk   string
}

type Executor interface {
    Preflight(ctx context.Context, n Node) error
    SetBootPXE(ctx context.Context, n Node) error
    PowerCycle(ctx context.Context, n Node) error
    // Observe returns the furthest install stage seen for this node.
    Observe(ctx context.Context, n Node) (Stage, error) // none|dhcp|imaging|done
}
```

- **Real**: `ipmiExecutor` (vendored `go-ipmi`) for power/bootdev; a
  pxeserver `observer` reading `dnsmasq.leases` + lighttpd access log by MAC.
- **Fake**: deterministic in-memory executor advancing stages over ticks —
  used by all engine/API tests. **No real BMC is ever contacted in CI.**

## Job engine

- One `Deploy` per cluster, holding per-node `NodeDeploy{state, stage, error,
  timestamps}`. Persisted to `<data-dir>/deploys/<clusterShortId>.json` so
  status survives restart.
- The engine runs each node's state machine in its own goroutine, stepping
  through the executor calls, polling `Observe` on an interval until `imaged`
  (or a timeout → error). Nodes are independent (one failing doesn't block
  others).
- Cancel stops stepping (does not forcibly power off — a running install is
  left alone; documented).

## The agent (`cmd/cube-cos-agent`)

A tiny static binary. On first boot of an unconfigured node (systemd unit,
gated on `/etc/appliance/state/configured` being absent):

1. Collect identity: all NIC MACs, serial (from `ip`/DMI).
2. Loop: `POST /api/v1/agents/checkin {macs, serial}` to the snapshot server
   (address from kernel cmdline `snapshot_server=` or a `--server` flag / the
   PXE server IP). Response: `{appointed, hostname, snapshotUrl, token}`.
3. When appointed: download the snapshot from `snapshotUrl`, run
   `hex_config snapshot_apply <file>` (the documented apply path), and
   `POST /api/v1/agents/report {hostname, state, message}` at each transition
   (applying → applied | error).
4. Exit/self-disable once applied (or once the node is configured).

Server address discovery, ret/backoff, and "already configured → no-op" are
handled in the agent. The agent has **no secrets** — it's given only its own
snapshot URL after the server matches its MACs to an appointment.

## REST API

| Method & path | Purpose |
| --- | --- |
| `GET /api/v1/clusters/{id}/deploy/plan` | Dry-run: per-node actions + the MACs/OS disk that would be used; 409 if any node unassigned |
| `POST /api/v1/clusters/{id}/deploy` | Start deploy. Body `{hostnames?: []}` to deploy a subset; requires `{confirm:true}` |
| `GET /api/v1/clusters/{id}/deploy` | Current job status (per-node state/stage) |
| `POST /api/v1/clusters/{id}/deploy/cancel` | Stop stepping |
| `POST /api/v1/agents/checkin` | Agent identity → match MACs to an appointment; returns `{appointed, hostname, snapshotUrl}` and advances that node to `checked-in` |
| `POST /api/v1/agents/report` | Agent progress (applying/applied/error) → advances the node's deploy state |

## Frontend

- **Deploy** button on the cluster page, enabled only when all nodes are
  assigned (tooltip explains what's missing otherwise).
- **Plan modal**: table of per-node actions (BMC, OS disk, MACs, steps) +
  "these servers will be power-cycled" warning + Confirm.
- **Deploy progress panel**: per-node state/stage with a small timeline and
  error surfacing; polls `GET …/deploy` while active. Re-openable from the
  cluster page.

## Build

`make all` builds the SPA, then both binaries: `bin/cube-cos-snapshot`
(embeds SPA) and `bin/cube-cos-agent`. Both static, vendored, air-gapped.

## Testing

- Go: engine drives fake executor through IPMI stages incl. an injected
  failure (preflight fail → node error, others proceed); agent check-in/report
  advances the matched node (checked-in → applied); MAC→appointment matching;
  plan precondition (unassigned → 409); persistence round-trip; API confirm
  gate. Agent: run loop against an httptest server (checkin → appointed →
  apply via an injected `applyFn` fake → report).
- Web: Deploy button enablement, plan modal contents, progress rendering from
  a mocked status.
- `scripts/smoke.sh`: deploy against the fake executor, then simulate an agent
  check-in + report and assert the node reaches `applied`.
- Real IPMI + real `hex_config snapshot_apply` are exercised only on the sky
  lab (follow-up), never CI.

## Out of scope (later)

The cubecos packaging change that installs the agent's systemd unit + kernel
cmdline (`snapshot_server=`) into the image; per-MAC pxelinux binding; auto
power-off on cancel; multi-cluster concurrency limits; agent auth token
hardening.
