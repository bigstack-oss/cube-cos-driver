# Zero-touch tripwire runbook

Goal: reimage the **1cc** then **3cc-sky** lab clusters hands-free, proving the
zero-touch flow (create snapshot → orchestrator PXE-reimages nodes → fabric
preflight → apply → healthy cluster) with no manual per-node config.

This runbook lists the supervised steps. The software is built, tested, and
committed; the outstanding work is the cubecos **image build** and the
**real-hardware reimage**, both of which need a human present (destructive power
ops + a first-ever end-to-end hardware run).

## What is done (software, validated here)

- `cube-cos-snapshot` server + `phone-home-agent` build; `go test ./...`, 47 web
  tests, and `scripts/smoke.sh` (GL1 barrier → apply → done) all green.
- cubecos integration committed: `core/snapshot-agent` (build + ship agent),
  `hex` `feat/zero-touch-orchestrator` (installer preflight + agent in PXE
  installer + installer pkgs).

## What is NOT yet validated (needs the lab)

- The cubecos pxe image build with the agent baked in (`make pxedeploy`).
- Real IPMI power/one-time-PXE on the actual boards.
- Real bond/VLAN/IP bring-up + carrier + ping matrix in the installer.
- SEL over local KCS (`/dev/ipmi0`) on the actual BMCs.
- The IF.N → ethX enumeration mapping on real NICs.

## Step 0 — publish the agent branch (so the image build can clone it)

The cubecos build clones the agent from GitHub. Until the work merges to the
default branch, push the feature branch and build with `AGENT_BRANCH`:

```
cd /root/cube-cos-snapshot && git push origin feat/orchestrator   # SSH remote
cd /root/cubecos && git push origin feat/zero-touch-orchestrator
cd /root/cubecos/hex && git push origin feat/zero-touch-orchestrator
```

(These were left committed-but-unpushed for your review.)

## Step 1 — build the cubecos pxe image with the agent

Clean build from scratch (per house rules), passing the agent branch through:

```
# in the jail; see CLAUDE.md "make clean cubecos"
make AGENT_BRANCH=feat/orchestrator pxedeploy PROJ_PXE_NAME=travis_cubecos
```

Verify the agent landed:
- OS image: `/usr/local/bin/phone-home-agent` + `phone-home-agent.service`
  enabled.
- PXE installer: `/usr/sbin/phone-home-agent` + `/usr/sbin/hex_autoinstall`.

## Step 2 — point Synology PXE at the new image + zero-touch cmdline

The lab already runs PXE/DHCP on the Synology NAS (10.32.0.200) on the flat
10.32.0.0/16. Reuse it (do not disable). Place the new `.pkg` + kernel/initrd
where Synology serves them, and set the PXE entry's kernel cmdline to include:

```
autoinstall snapshot_server=http://<dev-box-ip>:3299
```

`autoinstall` arms `hex_autoinstall`; `snapshot_server=` tells the agent where
the SPA is (both the installer preflight and the OS-phase apply).

## Step 3 — run the SPA server on the dev box

```
cd /root/cube-cos-snapshot && make all
./bin/cube-cos-snapshot --port 3299 --data-dir /var/lib/cube-cos-snapshot
# real hardware mode (IPMI executor + SEL observer) is automatic when
# --simulate is absent.
```

Open `http://<dev-box-ip>:3299`.

## Step 4 — inventory, snapshot, associate (per cluster)

In the UI, for the target cluster (1cc first):
1. **Hardware**: add each node's BMC (IP/user/pass — see the lab IPMI memory) or
   import from XLS/CSV; fetch hardware.
2. **Snapshot**: create/import the cluster snapshot (network + roles).
3. **Associate** each snapshot node → a BMC machine → OS disk → network map
   (drag NICs to bond/VLAN, tag mgmt/provider/overlay/storage). The mapper
   presets from the snapshot; correct IF.N picks against the fetched NICs.

## Step 5 — deploy (the tripwire)

Click **Deploy** → confirm. Watch per-node **light 1** (network preflight) and
**light 2** (apply) + phase. Expected sequence per node:

```
Boot from PXE → Network preflight (light1 yellow→green at GL1)
→ (restore+reboot) → Wait for master → Applying snapshot (light2)
→ Done (both green)
```

Green light 1 only clears once **every** node has passed preflight (carrier +
peer/gateway matrix + skew ≤ 5s). Apply is master-first: the master finishes FTS
before workers apply in parallel. If a node goes red, the error code
(`PF_CARRIER`, `PF_PING`, `PF_CLOCK_SKEW`, `BMC_*`, `APPLY_*`) says where.

**Success criterion:** all nodes reach Done and `cluster check` reports every
service group `ok`. Do 1cc first; only if it passes, repeat Steps 4–5 for
3cc-sky.

## Dry-run without hardware

The whole orchestration (minus real IPMI/apply) runs against a fake executor:

```
./bin/cube-cos-snapshot --port 3299 --simulate
bash scripts/smoke.sh    # scripted GL1 barrier → apply → done
```

## Safe first supervised check (non-destructive)

Before the destructive deploy, you can validate BMC reachability/creds only:
the orchestrator's BMC preflight (IPMI power status) and SEL-time read are
read-only. Add the BMC inventory and confirm each machine fetches hardware —
that exercises the IPMI path without powering or reimaging anything.
