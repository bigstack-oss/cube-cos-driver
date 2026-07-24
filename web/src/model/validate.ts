// Cluster-level validation rules, ported from the legacy
// DownloadNotification/ClusterNotification logic. `error` problems block
// saving/downloading (legacy isDanger); `success` entries mirror the legacy
// green confirmations.
import { roleOptions } from './roles'
import { allIFs, ClusterConfig, ifName, NodeConfig } from './types'

export type Problem = {
  level: 'error' | 'warning' | 'success'
  title: string
  text: string
}

export const sanitizeClusterName = (raw: string, fallback: string): string =>
  raw
    .replace(/^\.+/, '')
    .replaceAll('/', '')
    .replaceAll(/[?*\\"]/gi, '_')
    .replace(/^\s*$/, fallback)
    .substring(0, 20)

export const isValidIPv4 = (s: string): boolean => {
  const parts = s.split('.')
  if (parts.length !== 4) return false
  return parts.every((p) => /^\d{1,3}$/.test(p) && Number(p) <= 255)
}

export const isValidCIDR = (s: string): boolean => {
  const [ip, prefix, ...rest] = s.split('/')
  if (rest.length > 0 || !prefix) return false
  return isValidIPv4(ip) && /^\d{1,2}$/.test(prefix) && Number(prefix) <= 32
}

export const isValidHostname = (s: string): boolean =>
  /^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$/.test(s)

export const findDuplicatedHostname = (nodes: NodeConfig[]): string | null => {
  const seen = new Set<string>()
  for (const node of nodes) {
    if (seen.has(node.hostname)) return node.hostname
    seen.add(node.hostname)
  }
  return null
}

export type DuplicatedIF = {
  hostname1: string
  ifName1: string
  hostname2: string
  ifName2: string
  ipAddr: string
}

export const findDuplicatedIF = (nodes: NodeConfig[]): DuplicatedIF | null => {
  const byIP = new Map<string, { hostname: string; ifName: string }>()
  for (const node of nodes) {
    for (const IF of allIFs(node)) {
      if (!IF.IPAddr) continue
      const existing = byIP.get(IF.IPAddr)
      if (existing) {
        return {
          hostname1: node.hostname,
          ifName1: IF.name,
          hostname2: existing.hostname,
          ifName2: existing.ifName,
          ipAddr: IF.IPAddr,
        }
      }
      byIP.set(IF.IPAddr, { hostname: node.hostname, ifName: IF.name })
    }
  }
  return null
}

export const findInvalidVirtualHostname = (
  config: ClusterConfig,
  nodes: NodeConfig[],
): string | null => {
  if (!config.HA) return null
  return (
    nodes.find((n) => n.hostname === config.HASettings.virtualHostname)
      ?.hostname ?? null
  )
}

export const findInvalidVirtualIP = (
  config: ClusterConfig,
  nodes: NodeConfig[],
): { hostname: string; ifName: string; ipAddr: string } | null => {
  if (!config.HA) return null
  for (const node of nodes) {
    for (const IF of allIFs(node)) {
      if (IF.IPAddr && IF.IPAddr === config.HASettings.virtualIP) {
        return { hostname: node.hostname, ifName: IF.name, ipAddr: IF.IPAddr }
      }
    }
  }
  return null
}

// Standalone: exactly one control/control-converged/edge-core.
// HA: 3 control(-converged), or 3 edge-cores / 2 edge-cores + 1 moderator.
export const isRoleDistributionValid = (
  config: ClusterConfig,
  nodes: NodeConfig[],
): boolean => {
  const ctrlNum = nodes.filter((n) =>
    ['control', 'control-converged'].includes(n.role),
  ).length
  const edgeNum = nodes.filter((n) => n.role === 'edge-core').length
  const moderatorNum = nodes.filter((n) => n.role === 'moderator').length

  if (!config.HA && ctrlNum + edgeNum === 1) return true
  if (config.HA) {
    if (ctrlNum === 3 && edgeNum + moderatorNum === 0) return true
    if (ctrlNum === 0 && edgeNum + moderatorNum === 3 && moderatorNum <= 1)
      return true
  }
  return false
}

export const hasComputeRole = (nodes: NodeConfig[]): boolean =>
  nodes.some((n) =>
    ['compute', 'control-converged', 'edge-core'].includes(n.role),
  )

export const hasModeratorRole = (nodes: NodeConfig[]): boolean =>
  nodes.some((n) => n.role === 'moderator')

// A node's role interfaces must all resolve (except optional storIFBackend).
export const findIncompleteNode = (nodes: NodeConfig[]): string | null => {
  for (const node of nodes) {
    for (const key of roleOptions[node.role]) {
      const info = node.roleSettings[key]
      if (key === 'storIFBackend') {
        if (info?.id && ifName(node, info.id) === 'None') return node.hostname
        continue
      }
      if (!info?.id || ifName(node, info.id) === 'None') return node.hostname
    }
  }
  return null
}

export const validateCluster = (
  config: ClusterConfig,
  nodes: NodeConfig[],
): Problem[] => {
  const problems: Problem[] = []
  const push = (level: Problem['level'], title: string, text: string) =>
    problems.push({ level, title, text })

  const duplicatedHostname = findDuplicatedHostname(nodes)
  if (duplicatedHostname) {
    push(
      'error',
      'Hostname error',
      `Hostnames should not be duplicated, but ${duplicatedHostname} is used more than once.`,
    )
  } else {
    push('success', 'Hostname', 'All hostnames are unique.')
  }

  const dup = findDuplicatedIF(nodes)
  if (dup) {
    push(
      'error',
      'IP address error',
      `IP addresses should not be duplicated, but ${dup.ipAddr} is used by both ${dup.hostname1}/${dup.ifName1} and ${dup.hostname2}/${dup.ifName2}.`,
    )
  } else {
    push('success', 'IP address', 'All interface IP addresses are unique.')
  }

  if (config.HA) {
    const invalidVH = findInvalidVirtualHostname(config, nodes)
    if (invalidVH) {
      push(
        'error',
        'Virtual hostname error',
        `The virtual hostname must differ from every node hostname, but ${invalidVH} matches it.`,
      )
    }
    const invalidVIP = findInvalidVirtualIP(config, nodes)
    if (invalidVIP) {
      push(
        'error',
        'Virtual IP error',
        `The virtual IP must differ from every interface IP, but ${invalidVIP.hostname}/${invalidVIP.ifName} uses ${invalidVIP.ipAddr}.`,
      )
    }
  }

  if (isRoleDistributionValid(config, nodes)) {
    push(
      'success',
      config.HA ? 'High availability' : 'Standalone',
      config.HA
        ? 'The role distribution satisfies an HA configuration.'
        : 'There is a unique control, control-converged or edge-core node.',
    )
  } else {
    push(
      'error',
      config.HA ? 'High availability error' : 'Standalone error',
      config.HA
        ? 'HA requires 3 control/control-converged nodes, or 3 edge-cores, or 2 edge-cores + 1 moderator.'
        : 'In standalone, there must be exactly one control, control-converged or edge-core node.',
    )
  }

  if (!hasComputeRole(nodes)) {
    push(
      'error',
      'Compute error',
      'The cluster needs at least one node with compute capability (compute, control-converged or edge-core).',
    )
  }

  if (!config.HA && hasModeratorRole(nodes)) {
    push(
      'error',
      'Moderator error',
      'In standalone, moderators are not allowed. If you need a moderator, switch to high availability.',
    )
  }

  const incomplete = findIncompleteNode(nodes)
  if (incomplete) {
    push(
      'error',
      'Role interface error',
      `Node ${incomplete} has role interfaces that are not configured.`,
    )
  }

  return problems
}

// Legacy isDanger: any error blocks save/download.
export const isDanger = (
  config: ClusterConfig,
  nodes: NodeConfig[],
): boolean => validateCluster(config, nodes).some((p) => p.level === 'error')
