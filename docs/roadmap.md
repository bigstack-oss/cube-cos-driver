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

## Design constraint: no console

Serial-over-LAN is not available on all board families (e.g. the sky lab's
MegaRAC SP-X boards), so the orchestration design must never depend on
console interaction. It doesn't need to:

- **Power/boot control is plain IPMI** — `chassis power …` and one-time
  `bootdev pxe` work without SoL. No BIOS/installer interaction is needed;
  PXE imaging is unattended once the pxelinux entry is in place.
- **Imaging-phase observability comes from the pxeserver itself** — the
  orchestrator runs next to dnsmasq/lighttpd and sees DHCP leases (also the
  MAC↔node learning point), TFTP fetches, and `.pkg` HTTP downloads as
  progress signals. The PXE ramdisk runs sshd (root login), so active
  probing during this phase is SSH, not console.
- **Post-image progress comes from a phone-home agent** (below), replacing
  the console entirely. Human-only fallback for debugging remains the BMC
  web KVM; orchestration never depends on it.

## Zero-touch agent (the cubecos enabler)

CubeCOS permits running a service on an **unconfigured** node. Ship a small
zero-touch agent in the image: a systemd unit gated on the node being
unconfigured (e.g. `/etc/appliance/state/configured` absent), self-disabling
once configuration completes. On first boot it:

1. DHCPs on all links; collects identity (MACs, serial, NIC inventory).
2. Polls the snapshot server at a well-known address — the pxeserver IP it
   was imaged from, passed via kernel cmdline in the PXE entry (or DHCP
   option): *"here is who I am — is a snapshot appointed to me?"*
3. On appointment: runs **preflight** — configures candidate interfaces per
   the appointed `network1_0.yml`, pings gateway/peer mgmt IPs, reports
   results.
4. On preflight pass: downloads the snapshot, runs
   `hex_config snapshot_apply`, reports each state transition, disables
   itself.

Pull/phone-home beats a push model: no discovery problem, no long-lived
listening service on nodes, survives DHCP churn, and every state change is
an HTTP callback into the orchestrator's job engine — exactly the progress
stream the UI needs. It also simplifies **appointment**: phoned-home nodes
appear in the UI as discovered hardware (MAC/serial/NICs) for the user to
bind snapshots to (or pre-bind by MAC). Redfish inventory becomes an
enrichment, not a dependency.

**Trust note:** an unconfigured node accepting config from the network is a
deliberate trust decision. Gate strictly on "unconfigured" + the air-gapped
install network; a per-cluster token carried in the kernel cmdline is cheap
insurance worth adding.

## Gaps to build (this repo)

1. **Job engine** (`internal/orchestrator`): persisted, resumable per-node
   state machines (power-cycle → netboot → image → phone-home → preflight →
   apply snapshot → verify), fed by agent callbacks + pxeserver-side signals,
   exposed via new job endpoints and UI.
2. **Agent-facing API**: register/poll/report endpoints for the zero-touch
   agent; discovered-hardware model + snapshot appointment (MAC/serial
   binding) in the UI.
3. **BMC inventory + credentials** (`internal/bmc`): per-node BMC IP/user/
   password for power/bootdev control, stored in a separate per-cluster file —
   NOT inside `clusterDetail` (that schema stays legacy-compatible and gets
   exported; secrets must not ride along). Tight file perms at minimum; UI
   auth story needs revisiting once the app can power servers off.
4. **Node↔snapshot binding on the PXE side**: per-MAC `pxelinux.cfg/01-<mac>`
   entries and/or kernel args (`snapshot_server=…`, cluster token) written by
   the app, same pattern as `--export-dir`.

## Enabler outside this repo (cubecos)

The zero-touch agent package described above (plus its PXE-entry kernel
args). Bigger than a bare "consume a snapshot URL on first boot" hook, but it
solves discovery, preflight, and progress reporting in one stroke — and it is
the piece explicitly permitted on unconfigured nodes.

## First spike (before filing stories)

On the lab pxeserver + sky nodes:

1. **Phone-home loop prototype**: hand-rolled agent script on one freshly
   imaged, unconfigured node — identity report → appointment poll →
   `snapshot pull`/`snapshot_apply` → status callbacks. Proves the whole
   console-free loop and pins down what the productized agent needs.
2. **Per-MAC pxelinux binding**: per-node PXE entries with kernel args on the
   lab pxeserver; confirm MAC learning from dnsmasq leases.
3. **IPMI-only control check on MegaRAC SP-X**: power + one-time bootdev pxe
   without SoL (expected to work; confirm).
4. (Enrichment) Redfish NIC/MAC inventory quality on MegaRAC SP-X vs iDRAC8.

Items 1–2 shape the orchestrator design; 3 de-risks the sky boards; 4 is
optional polish.
