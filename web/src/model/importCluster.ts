// Rough structural validation of an imported clusterDetail.json, ported from
// the legacy checkClusterDetailValid. Ensures the file won't crash the app;
// per-field correctness is enforced by the wizards/validateCluster.
import { ClusterDetail } from './types'

type Obj = Record<string, unknown>

export type ImportResult =
  | { ok: true; detail: ClusterDetail }
  | { ok: false; message: string }

const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null

const fail = (message: string): ImportResult => ({ ok: false, message })

const checkNodeConfig = (node: unknown): string | null => {
  if (!isObj(node)) return 'nodeConfig is not an object.'
  for (const attr of ['id', 'hostname', 'defaultGateway', 'role']) {
    if (typeof node[attr] !== 'string') {
      return `${attr} of nodeConfig is invalid.`
    }
  }
  for (const attr of ['initIFs', 'bondIFs', 'vlanIFs']) {
    const ifs = node[attr]
    if (!Array.isArray(ifs)) return `${attr} of nodeConfig is invalid.`
    for (const IF of ifs) {
      if (!isObj(IF)) return `${attr} of nodeConfig is invalid.`
      for (const option of ['id', 'type', 'name']) {
        if (typeof IF[option] !== 'string') {
          return `${option} of ${attr} in nodeConfig is invalid.`
        }
      }
      if (typeof IF.enabled !== 'boolean') {
        return `enabled of ${attr} in nodeConfig is invalid.`
      }
    }
  }
  if (!isObj(node.roleSettings)) return 'roleSettings of nodeConfig is invalid.'
  if (!isObj(node.defaultIF)) return 'defaultIF of nodeConfig is invalid.'
  return null
}

const checkClusterConfig = (config: unknown): string | null => {
  if (!isObj(config)) return 'clusterConfig is not an object.'
  if (!Array.isArray(config.DNS) || config.DNS.some((d) => typeof d !== 'string')) {
    return 'The DNS of clusterConfig is invalid.'
  }
  if (!isObj(config.timezone)) return 'The timezone of clusterConfig is invalid.'
  if (typeof config.timezone.name !== 'string') {
    return 'The timezone name of clusterConfig is invalid.'
  }
  if (typeof config.timezone.offset !== 'number') {
    return 'The timezone offset of clusterConfig is invalid.'
  }
  if (!isObj(config.roleSettings)) {
    return 'The roleSettings of clusterConfig is invalid.'
  }
  for (const option of ['extIP', 'region', 'secretSeed', 'mgmtCIDR']) {
    if (typeof config.roleSettings[option] !== 'string') {
      return `${option} of roleSettings in clusterConfig is invalid.`
    }
  }
  if (typeof config.HA !== 'boolean') return 'The HA of clusterConfig is invalid.'
  if (!isObj(config.HASettings)) {
    return 'The HASettings of clusterConfig is invalid.'
  }
  if (config.HA) {
    for (const option of ['virtualIP', 'virtualHostname']) {
      if (typeof config.HASettings[option] !== 'string') {
        return `${option} of HASettings in clusterConfig is invalid.`
      }
    }
  } else if (Object.keys(config.HASettings).length > 0) {
    return 'HASettings should be empty when HA is disabled.'
  }
  return null
}

export const checkClusterDetailValid = (json: unknown): ImportResult => {
  if (!isObj(json)) return fail('clusterDetail is not an object.')
  const info = json.clusterInfo
  if (!isObj(info)) return fail('clusterInfo is not an object.')
  if (typeof info.name !== 'string') return fail('The name of clusterInfo is invalid.')
  if (typeof info.id !== 'string' || info.id.length < 12) {
    return fail('The id of clusterInfo is invalid.')
  }
  const configErr = checkClusterConfig(json.clusterConfig)
  if (configErr) return fail(configErr)
  if (!Array.isArray(json.nodeData)) return fail('nodeData is invalid.')
  for (const node of json.nodeData) {
    const err = checkNodeConfig(node)
    if (err) return fail(err)
  }
  return { ok: true, detail: json as unknown as ClusterDetail }
}
