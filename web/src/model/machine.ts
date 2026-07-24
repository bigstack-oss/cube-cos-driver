export type FetchState = 'idle' | 'fetching' | 'ok' | 'error'

export type NIC = {
  name?: string
  mac?: string
  speedMbps?: number
  up?: boolean
}

export type Disk = {
  name?: string
  model?: string
  sizeBytes?: number
  type?: string
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

export type Machine = {
  id: string
  label: string
  bmc: { address: string; username: string }
  hasPassword: boolean
  inventory?: HardwareInventory
  fetchState: FetchState
  fetchError?: string
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
