// clusterDetail JSON schema — unchanged from the legacy app so previously
// exported files import cleanly.

export type IFType = 'init' | 'bond' | 'vlan'

export type NodeRole =
  | 'control'
  | 'compute'
  | 'storage'
  | 'control-converged'
  | 'edge-core'
  | 'moderator'

export type IF = {
  id: string
  type: IFType
  name: string
  enabled: boolean
  IPAddr?: string
  IPMask?: string
  master?: string
  slaves?: string[]
}

export type IFInfo = {
  id?: string
  type?: IFType
}

export type NodeRoleSettings = {
  mgmtIF: IFInfo
  providerIF?: IFInfo
  overlayIF?: IFInfo
  storIF: IFInfo
  storIFBackend: IFInfo
}

export type NodeConfig = {
  id: string
  hostname: string
  initIFs: IF[]
  bondIFs: IF[]
  vlanIFs: IF[]
  defaultIF: IFInfo
  defaultGateway: string
  role: NodeRole
  roleSettings: NodeRoleSettings
}

export type Timezone = {
  name: string
  offset: number
}

export type ClusterRoleSettings = {
  extIP: string
  region: string
  secretSeed: string
  mgmtCIDR: string
}

export type HASettings = {
  virtualIP?: string
  virtualHostname?: string
}

export type ClusterConfig = {
  DNS: string[]
  timezone: Timezone
  roleSettings: ClusterRoleSettings
  HA: boolean
  HASettings: HASettings
}

export type ClusterInfo = {
  id: string
  name: string
}

export type ClusterDetail = {
  clusterInfo: ClusterInfo
  clusterConfig: ClusterConfig
  nodeData: NodeConfig[]
}

export type ClusterDigest = {
  id: string
  name: string
  nodes: string[]
}

export const shortId = (id: string): string => id.slice(-12) || '0'.repeat(12)

export const allIFs = (node: NodeConfig): IF[] => [
  ...node.initIFs,
  ...node.bondIFs,
  ...node.vlanIFs,
]

export const ifName = (node: NodeConfig, id: string | undefined): string =>
  allIFs(node).find((f) => f.id === id)?.name ?? 'None'
