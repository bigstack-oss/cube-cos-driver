// Pure state transitions for the node wizard's interface editor, ported from
// the legacy NodeWizard handlers.
import { IF, IFInfo, NodeConfig, NodeRole } from '../../../model/types'
import { createDefaultRoleSettings, RoleIFKey, roleOptions } from '../../../model/roles'
import { NodeRoleSettings } from '../../../model/types'

export type IFDraft = {
  initIFs: IF[]
  bondIFs: IF[]
  vlanIFs: IF[]
}

export const allDraftIFs = (d: IFDraft): IF[] => [
  ...d.initIFs,
  ...d.bondIFs,
  ...d.vlanIFs,
]

export const newInitIF = (id: string, index: number): IF => ({
  id,
  type: 'init',
  name: `IF.${index}`,
  enabled: false,
})

// An IF is selectable as a role/default interface when it is enabled, not a
// bond slave, and (for mgmt/default) has an IP configured.
export const isIFSelectable = (
  IF: IF | undefined,
  option: RoleIFKey | 'defaultIF',
): boolean => {
  if (!IF || !IF.enabled) return false
  if (IF.type === 'init' && IF.master) return false
  if ((option === 'mgmtIF' || option === 'defaultIF') && !IF.IPAddr) {
    return false
  }
  return true
}

export const resizeInitIFs = (
  d: IFDraft,
  count: number,
  makeId: () => string,
): IFDraft => {
  if (count < 1 || count > 100) return d
  if (d.initIFs.length < count) {
    const initIFs = [...d.initIFs]
    while (initIFs.length < count) {
      initIFs.push(newInitIF(makeId(), initIFs.length + 1))
    }
    return { ...d, initIFs }
  }
  const removed = d.initIFs.filter((_, i) => i >= count).map((f) => f.id)
  return removeIFs(d, removed)
}

// Removing IFs cascades: bonds whose slaves all vanish, VLANs whose master
// vanishes; surviving bonds lose removed slaves, init IFs lose dangling
// masters.
export const removeIFs = (d: IFDraft, ids: string[]): IFDraft => {
  const gone = new Set(ids)
  for (const bond of d.bondIFs) {
    if ((bond.slaves ?? []).every((s) => gone.has(s))) gone.add(bond.id)
  }
  for (const vlan of d.vlanIFs) {
    if (vlan.master && gone.has(vlan.master)) gone.add(vlan.id)
  }
  return {
    initIFs: d.initIFs
      .filter((f) => !gone.has(f.id))
      .map((f) => ({
        ...f,
        master: f.master && !gone.has(f.master) ? f.master : undefined,
      })),
    bondIFs: d.bondIFs
      .filter((f) => !gone.has(f.id))
      .map((f) => ({
        ...f,
        slaves: (f.slaves ?? []).filter((s) => !gone.has(s)),
      })),
    vlanIFs: d.vlanIFs.filter(
      (f) => !gone.has(f.id) && !(f.master && gone.has(f.master)),
    ),
  }
}

const bondNamePool = ['mgmt', 'data', 'ext', 'stor', 'backend']

export const createBond = (
  d: IFDraft,
  slaveIds: string[],
  bondId: string,
): IFDraft => {
  const name =
    bondNamePool.find((n) => d.bondIFs.every((b) => b.name !== n)) ??
    'newbdname'
  return {
    initIFs: d.initIFs.map((f) =>
      slaveIds.includes(f.id)
        ? {
            ...f,
            master: bondId,
            IPAddr: undefined,
            IPMask: undefined,
            enabled: true,
          }
        : f,
    ),
    bondIFs: [
      ...d.bondIFs,
      { id: bondId, type: 'bond', name, slaves: slaveIds, enabled: true },
    ],
    // VLANs directly on enslaved init IFs are dropped.
    vlanIFs: d.vlanIFs.filter(
      (f) => !(f.master && slaveIds.includes(f.master)),
    ),
  }
}

type VlanIF = IF & { vlanID?: number }

export const createVlan = (d: IFDraft, parent: IF, vlanId: string): IFDraft => {
  let vlanTag = 1
  while ((d.vlanIFs as VlanIF[]).some((f) => f.vlanID === vlanTag)) vlanTag++
  const vlan: VlanIF = {
    id: vlanId,
    type: 'vlan',
    name: `${parent.name}.${vlanTag}`,
    master: parent.id,
    vlanID: vlanTag,
    enabled: true,
  }
  return { ...d, vlanIFs: [...d.vlanIFs, vlan] }
}

export const changeIF = (
  d: IFDraft,
  target: IF,
  patch: Partial<VlanIF>,
): IFDraft => {
  const ifName = (id: string | undefined) =>
    allDraftIFs(d).find((f) => f.id === id)?.name ?? 'None'
  if (target.type === 'vlan' && patch.vlanID !== undefined) {
    patch = { ...patch, name: `${ifName(target.master)}.${patch.vlanID}` }
  }
  const apply = (list: IF[]) =>
    list.map((f) => (f.id === target.id ? { ...f, ...patch } : f))
  let next: IFDraft = {
    initIFs: target.type === 'init' ? apply(d.initIFs) : d.initIFs,
    bondIFs: target.type === 'bond' ? apply(d.bondIFs) : d.bondIFs,
    vlanIFs: target.type === 'vlan' ? apply(d.vlanIFs) : d.vlanIFs,
  }
  if (target.type === 'bond' && patch.name !== undefined) {
    next = {
      ...next,
      vlanIFs: next.vlanIFs.map((v) => {
        const vlan = v as VlanIF
        return vlan.master === target.id
          ? { ...vlan, name: `${patch.name}.${vlan.vlanID}` }
          : vlan
      }),
    }
  }
  return next
}

// Drop role-interface selections that became invalid after IF edits
// (legacy pruning effect).
export const pruneRoleSettings = (
  d: IFDraft,
  role: NodeRole,
  settings: NodeRoleSettings,
): NodeRoleSettings => {
  const ifs = allDraftIFs(d)
  const pruned: Record<string, IFInfo> = {}
  for (const key of roleOptions[role]) {
    const info = settings[key]
    if (!info?.id) {
      pruned[key] = {}
      continue
    }
    const IF = ifs.find((f) => f.id === info.id)
    pruned[key] = isIFSelectable(IF, key) ? info : {}
  }
  return pruned as unknown as NodeRoleSettings
}

export const pruneDefaultIF = (d: IFDraft, defaultIF: IFInfo): IFInfo => {
  if (!defaultIF.id) return defaultIF
  const IF = allDraftIFs(d).find((f) => f.id === defaultIF.id)
  return isIFSelectable(IF, 'defaultIF') ? defaultIF : {}
}

export const defaultNodeDraft = (makeId: () => string): NodeConfig => ({
  id: '',
  hostname: '',
  initIFs: [newInitIF(makeId(), 1)],
  bondIFs: [],
  vlanIFs: [],
  defaultIF: {},
  defaultGateway: '192.168.0.254',
  role: 'control',
  roleSettings: createDefaultRoleSettings('control'),
})
