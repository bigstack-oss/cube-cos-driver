// Reads the snapshot's node config into role chains for the binding mapper,
// and rewrites interface labels when the operator corrects a pick using real
// NIC info (e.g. IF.4 -> IF.5). CubeCOS assigns IF.N by PCI order, so the
// "binding" is a relabel of the base physical interface the topology uses.
import { RoleIFKey, roleOptions } from '../../../model/roles'
import { allIFs, IF, NodeConfig } from '../../../model/types'

export type RoleChain = {
  role: RoleIFKey
  // The base physical interface (init IF) the chain roots on, if resolved.
  baseIf?: IF
  // The bond sitting between base and role/vlan, if any.
  bond?: IF
  // The VLAN carrying this role, if any.
  vlan?: IF
  // The interface actually bound to the role (vlan ?? bond ?? base).
  roleIf?: IF
  // IP of the role interface, if configured.
  ip?: string
}

const findIF = (node: NodeConfig, id?: string): IF | undefined =>
  id ? allIFs(node).find((f) => f.id === id) : undefined

// buildChains resolves, per role, the base physical interface → optional bond
// → optional VLAN → role, plus the IP on the role interface.
export const buildChains = (node: NodeConfig): RoleChain[] => {
  return roleOptions[node.role]
    .filter((key) => key !== 'storIFBackend' || node.roleSettings.storIFBackend?.id)
    .map((key): RoleChain => {
      const roleIf = findIF(node, node.roleSettings[key]?.id)
      let base = roleIf
      let bond: IF | undefined
      let vlan: IF | undefined
      if (roleIf?.type === 'vlan') {
        vlan = roleIf
        const master = findIF(node, roleIf.master)
        if (master?.type === 'bond') {
          bond = master
          base = master.slaves?.length ? findIF(node, master.slaves[0]) : undefined
        } else {
          base = master
        }
      } else if (roleIf?.type === 'bond') {
        bond = roleIf
        base = roleIf.slaves?.length ? findIF(node, roleIf.slaves[0]) : undefined
      }
      return { role: key, baseIf: base, bond, vlan, roleIf, ip: roleIf?.IPAddr }
    })
}

// The distinct base physical interfaces the topology binds to (drop targets).
export const baseInterfaces = (node: NodeConfig): IF[] => {
  const seen = new Set<string>()
  const out: IF[] = []
  for (const c of buildChains(node)) {
    if (c.baseIf && !seen.has(c.baseIf.id)) {
      seen.add(c.baseIf.id)
      out.push(c.baseIf)
    }
  }
  return out
}

// bindPort relabels a base init interface to IF.<portIndex+1> (1-based),
// cascading the new label to any VLANs that sit directly on it.
export const bindPort = (
  node: NodeConfig,
  baseIfId: string,
  portIndex: number,
): NodeConfig => {
  const newName = `IF.${portIndex + 1}`
  const base = allIFs(node).find((f) => f.id === baseIfId)
  if (!base) return node
  const oldName = base.name
  const renameInit = (f: IF): IF => (f.id === baseIfId ? { ...f, name: newName } : f)
  const renameVlan = (f: IF): IF =>
    f.master === baseIfId && f.name.startsWith(`${oldName}.`)
      ? { ...f, name: f.name.replace(`${oldName}.`, `${newName}.`) }
      : f
  return {
    ...node,
    initIFs: node.initIFs.map(renameInit),
    vlanIFs: node.vlanIFs.map(renameVlan),
  }
}
