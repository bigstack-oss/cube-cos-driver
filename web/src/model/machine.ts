export type FetchState = 'idle' | 'fetching' | 'ok' | 'error'

export type NIC = {
  name?: string
  mac?: string
  pciAddr?: string
  pciVendor?: string
  speedMbps?: number
  up?: boolean // interface operstate up
  carrier?: boolean // physical link (cable) up; undefined = unknown
}

const PCI_VENDORS: Record<string, string> = {
  '0x8086': 'Intel',
  '0x8087': 'Intel',
  '0x15b3': 'Mellanox',
  '0x14e4': 'Broadcom',
  '0x1077': 'QLogic',
  '0x19a2': 'Emulex',
  '0x1924': 'Solarflare',
  '0x1425': 'Chelsio',
  '0x10ec': 'Realtek',
  '0x1af4': 'virtio',
  '0x1137': 'Cisco',
  '0x10de': 'NVIDIA',
}

export const pciVendorName = (id?: string): string =>
  id ? (PCI_VENDORS[id.toLowerCase()] ?? id) : ''

// pciLabel renders "<vendor> <bus-id>" for a NIC (either part optional).
export const pciLabel = (n: NIC): string =>
  [pciVendorName(n.pciVendor), n.pciAddr].filter(Boolean).join(' ') || '—'

export const formatSpeed = (mbps?: number): string =>
  !mbps || mbps <= 0
    ? '—'
    : mbps >= 1000
      ? `${mbps / 1000} Gb/s`
      : `${mbps} Mb/s`

export type Disk = {
  name?: string
  model?: string
  sizeBytes?: number
  type?: string
  tran?: string // transport: sata | sas | nvme | iscsi | fc | usb
  osEligible?: boolean // agent-computed: safe as a CubeCOS OS-install target
}

// A disk is a valid CubeCOS OS-install target only if it is a local physical
// disk — never a SAN LUN (iSCSI/FC, e.g. Dell Compellent) or BMC virtual media.
// Prefer the agent's computed flag; fall back to a transport/model heuristic so
// inventory captured before the agent reported osEligible is still filtered.
const SAN_OR_VIRTUAL =
  /compellent|virtual|idrac|vdvd|dvd|cd-?rom|iscsi|multipath|mpath|3par|\bmsa\b|nimble|unity|powerstore|vnx|netapp|\bpure\b|eternus/i

export const isOsEligible = (d: Disk): boolean => {
  if (typeof d.osEligible === 'boolean') return d.osEligible
  const tran = (d.tran ?? '').toLowerCase()
  if (tran === 'iscsi' || tran === 'fc' || tran === 'fcoe') return false
  if (SAN_OR_VIRTUAL.test(`${d.model ?? ''} ${d.name ?? ''}`)) return false
  return (d.sizeBytes ?? 0) > 0
}

export const osEligibleDisks = (inv?: HardwareInventory): Disk[] =>
  (inv?.disks ?? []).filter(isOsEligible)

// diskMediaType is a human label for where a disk lives: a local physical disk
// (valid OS target) vs. a SAN LUN (iSCSI/FC) or BMC virtual media (not valid).
// Transport-first, with a model fallback for inventory captured before `tran`.
export const diskMediaType = (d: Disk): string => {
  const tran = (d.tran ?? '').toLowerCase()
  const hay = `${d.model ?? ''} ${d.name ?? ''}`.toLowerCase()
  // HW-RAID virtual disk (explicitly OS-eligible from discovery) — label it
  // before the generic /virtual/ checks would call it virtual media.
  if (d.osEligible === true && /raid virtual disk/.test(hay)) return 'RAID · virtual disk'
  if (d.osEligible === false && (tran === '' || tran === 'sas' || tran === 'sata') && !/virtual|compellent|usb/.test(hay))
    return 'RAID member'
  if (tran === 'iscsi') return 'SAN · iSCSI'
  if (tran === 'fc' || tran === 'fcoe') return 'SAN · FC'
  if (/virtual|vdvd|\bidrac\b|vdisk/.test(hay)) return 'Virtual media'
  if (/compellent|3par|\bmsa\b|nimble|unity|powerstore|vnx|netapp|\bpure\b|eternus/.test(hay))
    return 'SAN'
  if (tran === 'usb') return 'USB / virtual'
  if (tran) return `Local · ${tran.toUpperCase()}`
  return 'Local'
}

export type Card = {
  slot?: string
  name?: string
  type?: string
}

export type HardwareInventory = {
  fetchedAt: string
  source: string
  manufacturer?: string
  model?: string
  serial?: string
  cpuModel?: string
  cpuCount?: number
  cpuCores?: number
  memoryBytes?: number
  nics?: NIC[]
  disks?: Disk[]
  cards?: Card[]
}

export type Assignment = {
  clusterId: string
  hostname: string
  osDisk?: string
}

export type Machine = {
  id: string
  label: string
  bmc: { address: string; username: string }
  hasPassword: boolean
  inventory?: HardwareInventory
  fetchState: FetchState
  fetchError?: string
  assignment?: Assignment // primary (first) — back-compat
  assignments?: Assignment[]
}

export type MachineInput = {
  label: string
  bmc: { address: string; username: string; password?: string }
}

export const formatBytes = (bytes?: number): string => {
  if (!bytes) return '—'
  const gib = bytes / 1024 ** 3
  if (gib >= 1024) return `${(gib / 1024).toFixed(1)} TiB`
  return `${gib.toFixed(gib < 10 ? 1 : 0)} GiB`
}
