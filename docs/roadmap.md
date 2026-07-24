# Roadmap: zero-touch cluster installation

Beyond generating snapshots, the app's end goal is to **orchestrate CubeCOS
cluster installation with zero touch**: once cluster snapshots are created
and each snapshot is appointed to a server node, the app drives the servers
through BMC/IPMI — power-cycle into netboot, PXE re-image, and have each
node consume its appointed snapshot unattended — including preflight
network-connectivity checks before committing an install.

The current architecture (single Go binary on the pxeserver + SPA + file
store) supports this direction; the work is additive. Recorded here so the
follow-up spikes/stories have a shared reference.

## Why the architecture fits

- The orchestrator must live where the app already runs: on the pxeserver,
  same L2 as the nodes, next to dnsmasq/dhcpd/tftp/lighttpd.
- Long-running concurrent per-node workflows are plain goroutines; no extra
  runtime or queue infra at this scale.
- Pure-Go BMC clients (e.g. `bougou/go-ipmi` for IPMI lanplus chassis/bootdev,
  `stmcginnis/gofish` for Redfish) keep the single-static-binary and
  air-gapped properties — no `ipmitool` in the image.
- `clusterDetail` already models the desired per-node state (NICs, IPs,
  roles); snapshots are already exported where netbooted nodes fetch them.
- The PXE ramdisk runs sshd with root login — preflight interface
  configuration and connectivity tests can be driven over SSH with no new
  node-side agent.

## Gaps to build (this repo)

1. **Job engine** (`internal/orchestrator`): persisted, resumable per-node
   state machines (power-cycle → netboot → image → first boot → apply
   snapshot → verify), progress exposed via new job endpoints and UI.
2. **BMC inventory + credentials** (`internal/bmc`): per-node BMC IP/user/
   password, stored in a separate per-cluster file — NOT inside
   `clusterDetail` (that schema stays legacy-compatible and gets exported;
   secrets must not ride along). Tight file perms at minimum; UI auth story
   needs revisiting once the app can power servers off.
3. **Hardware discovery**: plain IPMI cannot read host NICs (only BMC LAN
   config + FRU). Plan: Redfish first (MegaRAC SP-X boards support it;
   iDRAC8 is limited), with a BMC-independent fallback — learn MAC↔node from
   dnsmasq DHCP leases during first netboot, then SSH into the ramdisk for
   full `ip link` inventory.
4. **Node↔snapshot binding**: per-MAC `pxelinux.cfg/01-<mac>` entries and/or
   kernel args (`snapshot=<hostname>`) written by the app, same pattern as
   `--export-dir`.

## Enabler outside this repo (cubecos)

`snapshot pull` / `snapshot apply` are interactive hex_cli flows today.
Zero-touch needs an unattended consume path on the node, e.g. kernel
cmdline / `rc.postinstall`: `snapshot_url=…` → download → `hex_config
snapshot_apply` on first boot. This cubecos change is the critical enabler;
everything else is app-side plumbing.

## First spike (before filing stories)

On the lab pxeserver + sky MegaRAC boards:

1. Verify Redfish NIC/MAC inventory quality on MegaRAC SP-X (and what iDRAC8
   can/can't provide).
2. Prototype per-MAC pxelinux binding + a hand-rolled unattended
   `snapshot_url` consume on one node.

These two facts shape the orchestrator design more than anything else.
