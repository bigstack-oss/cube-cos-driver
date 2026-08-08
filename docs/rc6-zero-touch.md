# Zero-touch deploy of a released image that predates the agent

How `v3.1.0-rc6` — released long before zero-touch existed — is deployed by
cube-cos-driver without rebuilding or modifying the release.

Written up because the artifacts are hand-derived: if `/mnt/pxe/v3.1.0-rc6-zt/`
is lost, nothing in any repo reproduces it.

## Why not just rebuild rc6 with the agent in it

Two reasons, and the second is fatal:

1. Rebuilding contradicts the premise. The point of an rc6 baseline is to test
   an upgrade *from the released bits*. A rebuild is a different image.
2. **It is not reproducible anyway.** The build jail pins the shipped kernel
   (`6.12.74-1.el9`), and that RPM is no longer on the upstream mirror — only
   `6.12.96` and later. Any rebuild today silently ships a different kernel.
   Tracked by cubecos#1234 (archive the jail image + kernel RPMs per release).

So nothing is rebuilt. Only the **installer ramdisk** is patched; the released
`.pkg` that gets restored to disk stays byte-for-byte identical.

## Layout on the PXE server

```
/mnt/pxe/v3.1.0-rc6/        <- the untouched release. NEVER modified.
/mnt/pxe/v3.1.0-rc6-zt/
    vmlinuz                 <- byte copy of the rc6 kernel
    initrd.img              <- the ONLY modified artifact (two patches, below)
    pxe.cfg                 <- PKG_SERVER=nfs://10.32.0.200:/volume1/pxe-server/v3.1.0-rc6
```

`pxe.cfg` is what keeps the release pristine: the `-zt` entry boots a patched
installer that then restores the **original** rc6 `.pkg` (~11 GB, never copied,
never touched).

GRUB entry in `/mnt/pxe/grub.cfg`:

```
menuentry 'v3.1.0-rc6-zt zero-touch (UEFI)' { linuxefi /v3.1.0-rc6-zt/vmlinuz rw root=/dev/ram0 ramdisk_size=1084780KB net.ifnames=0 init= console=tty0 console=ttyS0 quiet erst_disable pxe_via_nfs=10.32.0.200:/volume1/pxe-server/v3.1.0-rc6 ; initrdefi /v3.1.0-rc6-zt/initrd.img }
```

`ramdisk_size` must fit the *uncompressed* ramdisk — it is larger here than the
neighbouring 3.1.10 entries because this initrd carries the agent. Leave `set
default` alone; the driver flips the default per deploy and restores it.

## The two patches inside the initrd

### 1. `usr/sbin/hex_autoinstall` — install the agent into the restored rootfs

rc6's rootfs has no agent binary and no unit, and repacking a 10 GB
`rootfs.cgz` to add them would defeat the purpose. Instead the installer drops
both into the freshly restored rootfs at `$mnt`, just before it finishes:

```sh
# --- rc6 backport (not in upstream hex) -------------------
src=/run/phone-home-agent.fresh
[ -s "$src" ] || src=/usr/sbin/phone-home-agent
if [ -s "$src" ]; then
    install -D -m 0755 "$src" "$mnt/usr/local/bin/phone-home-agent"
    install -D -m 0755 "$src" "$mnt/usr/sbin/phone-home-agent"
fi
mkdir -p "$mnt/usr/lib/systemd/system" \
         "$mnt/etc/systemd/system/multi-user.target.wants"
cat > "$mnt/usr/lib/systemd/system/phone-home-agent.service" <<'PHA_UNIT'
... unit ...
PHA_UNIT
ln -sf /usr/lib/systemd/system/phone-home-agent.service \
    "$mnt/etc/systemd/system/multi-user.target.wants/phone-home-agent.service"
# --- end rc6 backport -------------------------------------
```

Two details that matter:

- `/run/phone-home-agent.fresh` is preferred over the copy baked into the
  ramdisk. `hex_pxe_fetch` hot-fetches the current agent from the driver
  **before** preflight reconfigures the network, so the installed node gets the
  agent the driver is serving now, not whatever was frozen into this initrd.
- The unit carries `ConditionPathExists=!/etc/appliance/state/configured`, so it
  runs on the unconfigured first boot and stays inert forever after.

### 2. `etc/rc.d/rc.local` — stop duplicating the fetch

rc6's `rc.local` carried its own inline fetch block that predates and conflicts
with `hex_pxe_fetch`. It is deleted; the file now ends with only:

```sh
touch /var/lock/subsys/local

/usr/sbin/hex_pxe_fetch
```

## Rebuilding the initrd

Plain gzip + newc cpio. Note that cpio does **not** read concatenated archives —
appending a second `.cgz` does not work; it stops at the first trailer. Unpack,
edit, repack:

```sh
mkdir -p /root/rc6-zt/unpack && cd /root/rc6-zt/unpack
pigz -cd /mnt/pxe/v3.1.0-rc6/initrd.img | cpio -iumd --quiet

# apply the two patches above to usr/sbin/hex_autoinstall and etc/rc.d/rc.local,
# and drop the agent at usr/sbin/phone-home-agent (mode 0755)

find . | cpio -o -H newc --quiet | pigz -9 > /root/rc6-zt/initrd.img
```

Then publish (`vmlinuz` is a straight copy from the release):

```sh
install -D -m 0644 /root/rc6-zt/initrd.img /mnt/pxe/v3.1.0-rc6-zt/initrd.img
cp /mnt/pxe/v3.1.0-rc6/vmlinuz            /mnt/pxe/v3.1.0-rc6-zt/vmlinuz
printf 'PKG_SERVER=nfs://10.32.0.200:/volume1/pxe-server/v3.1.0-rc6\n' \
    > /mnt/pxe/v3.1.0-rc6-zt/pxe.cfg
```

Add the GRUB entry above and verify it appears in `GET /api/v1/pxe/images`.

## Pre-flight before an rc6 deploy

rc6 predates two fixes the agent now works around. The marker one is automatic;
the timezone one is a config choice you must get right:

- **Timezone must have two components** (`Asia/Taipei`, not `UTC`). Pre-fix
  `config_horizon` splits the zone on `/` and reads past the end of the vector
  for a single-component zone, segfaulting `hex_config` mid-commit. The agent
  now refuses such an apply up front, but the cheaper check is before you start:

  ```sh
  curl -s $DRIVER/api/v1/clusters/$ID | jq -r .clusterConfig.timezone.name
  ```

- **State markers** are handled automatically: on images `<= 3.1.0` the agent
  strips `etc/appliance/state/{configured,sla_accepted}` from a copy of the
  snapshot, applies that, and stamps them back on success. Nothing to configure;
  look for `legacy-markers:` in the agent journal to confirm it fired.

## Verifying you got the release, not a rebuild

```sh
cat /etc/version   # CUBE_3.1.0_20260313-1639_f58d033
uname -r           # 6.12.74-1.el9.x86_64
```

A different kernel means something rebuilt the image — see the reproducibility
note above.
