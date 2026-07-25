// Per-node network-topology diagram for config review: physical NIC → bond →
// VLAN → role → IP, plus the node's disks (resolved after hardware
// association). Lets an operator confirm interface mapping / bonds / VLANs /
// roles / addresses match intent before deploy.
import { ClusterConfig, NodeConfig } from '../../model/types'
import { Machine } from '../../model/machine'
import { RoleIFKey } from '../../model/roles'
import { buildChains, RoleChain } from './assign/snapshotTopology'

export type NodeTopologyProps = {
  node: NodeConfig
  cluster: ClusterConfig
  machine?: Machine
}

const ROLE: Record<RoleIFKey, { name: string; color: string; bg: string }> = {
  mgmtIF: { name: 'Management', color: '#4C68F9', bg: 'rgba(76,104,249,.12)' },
  providerIF: { name: 'Provider', color: '#2AA7D8', bg: 'rgba(42,167,216,.12)' },
  overlayIF: { name: 'Overlay', color: '#00B98C', bg: 'rgba(0,185,140,.14)' },
  storIF: { name: 'Storage', color: '#D98A1F', bg: 'rgba(217,138,31,.14)' },
  storIFBackend: { name: 'Storage backend', color: '#9B426E', bg: 'rgba(155,66,110,.14)' },
}

const vlanTag = (name: string): string => {
  const i = name.lastIndexOf('.')
  return i >= 0 ? name.slice(i + 1) : ''
}

const maskToPrefix = (mask?: string): string => {
  if (!mask) return ''
  const p = mask
    .split('.')
    .reduce((n, o) => n + ((parseInt(o, 10) >>> 0).toString(2).match(/1/g)?.length ?? 0), 0)
  return '/' + p
}

const fmtSize = (b?: number): string =>
  !b ? '' : b >= 1e12 ? (b / 1e12).toFixed(1) + ' TB' : Math.round(b / 1e9) + ' GB'

const Arrow = () => (
  <svg width="18" height="12" viewBox="0 0 18 12" className="shrink-0 text-functional-border-divider">
    <path d="M1 6h13m0 0l-4-4m4 4l-4 4" stroke="currentColor" strokeWidth="1.4" fill="none" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
)

const Chip = ({ label, sub }: { label: string; sub?: string }) => (
  <div className="flex flex-col rounded-lg border border-functional-border-divider bg-functional-background-disable px-2.5 py-1">
    <span className="primary-body4 font-mono font-semibold leading-tight">{label}</span>
    {sub && <span className="secondary-body6 text-functional-text-light leading-tight">{sub}</span>}
  </div>
)

const ChainRow = ({ chain, gateway, defaultIfId }: { chain: RoleChain; gateway: string; defaultIfId?: string }) => {
  const r = ROLE[chain.role]
  const base = chain.baseIf
  const isDefault = chain.roleIf?.id === defaultIfId
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-2 py-2.5">
      {/* physical base NIC */}
      <Chip label={base?.name ?? '—'} sub="physical NIC" />
      {chain.bond && (
        <>
          <Arrow />
          <Chip label={chain.bond.name} sub="bond" />
        </>
      )}
      {chain.vlan && (
        <>
          <Arrow />
          <Chip label={chain.vlan.name} sub={`VLAN ${vlanTag(chain.vlan.name)}`} />
        </>
      )}
      <Arrow />
      {/* role badge */}
      <span
        className="inline-flex items-center gap-x-1.5 rounded-full px-2.5 py-1 primary-body4 font-semibold whitespace-nowrap"
        style={{ color: r.color, background: r.bg }}
      >
        <span className="h-2 w-2 rounded-full" style={{ background: r.color }} />
        {r.name}
      </span>
      <div className="flex-1" />
      {/* address */}
      <div className="text-right">
        {chain.ip ? (
          <div className="font-mono primary-body4 font-semibold tabular-nums">
            {chain.ip}
            {maskToPrefix(chain.roleIf?.IPMask)}
          </div>
        ) : (
          <div className="secondary-body6 italic text-functional-text-light">uplink · no host IP</div>
        )}
        {isDefault && gateway && (
          <div className="secondary-body6 font-mono text-functional-text-light">gw {gateway} · default</div>
        )}
      </div>
    </div>
  )
}

export const NodeTopology = (props: NodeTopologyProps) => {
  const { node, cluster, machine } = props
  const chains = buildChains(node)
  const unused = node.initIFs.filter((i) => !i.enabled).map((i) => i.name)
  const disks = machine?.inventory?.disks ?? []
  const osDisk = machine?.assignment?.osDisk

  return (
    <div className="overflow-hidden rounded-2xl border border-functional-border-divider bg-functional-background-default">
      {/* header */}
      <div className="flex items-center gap-x-3 border-b border-functional-border-divider bg-functional-background-hover px-5 py-3">
        <span className="primary-h4">{node.hostname}</span>
        <span className="rounded-full border border-functional-border-divider px-2.5 py-0.5 secondary-body5 font-semibold text-functional-text-secondary">
          {node.role}
        </span>
        <div className="flex-1" />
        {cluster.HA && cluster.HASettings.virtualIP && (
          <span className="secondary-body5 font-mono text-functional-text-light">
            VIP <span style={{ color: '#4C68F9' }} className="font-semibold">{cluster.HASettings.virtualIP}</span>
          </span>
        )}
      </div>

      {/* interface chains */}
      <div className="divide-y divide-functional-border-divider px-5">
        {chains.map((c) => (
          <ChainRow key={c.role} chain={c} gateway={node.defaultGateway} defaultIfId={node.defaultIF?.id} />
        ))}
      </div>

      {/* unused + disks */}
      <div className="flex flex-col gap-y-3 border-t border-functional-border-divider px-5 py-3">
        {unused.length > 0 && (
          <div className="secondary-body6 text-functional-text-light">
            <span className="font-mono font-semibold">{unused.join(' · ')}</span> — unused
          </div>
        )}
        <div>
          <div className="secondary-body6 mb-1.5 font-semibold uppercase tracking-wide text-functional-text-light">
            Disks {disks.length === 0 && <span className="normal-case">· resolved after hardware association</span>}
          </div>
          {disks.length > 0 ? (
            <div className="flex flex-wrap gap-x-2 gap-y-2">
              {disks.map((d) => {
                const name = (d.name ?? '').replace('/dev/', '')
                const isOs = !!osDisk && (name === osDisk || '/dev/' + name === osDisk)
                return (
                  <div
                    key={name}
                    className="flex items-center gap-x-2 rounded-lg border px-3 py-1.5"
                    style={{ borderColor: isOs ? '#4C68F9' : undefined }}
                  >
                    <span
                      className="secondary-body6 font-semibold uppercase"
                      style={{ color: isOs ? '#4C68F9' : undefined }}
                    >
                      {isOs ? 'OS' : 'Data'}
                    </span>
                    <span className="font-mono primary-body4 font-semibold">/dev/{name}</span>
                    {d.sizeBytes && <span className="secondary-body6 font-mono text-functional-text-light">{fmtSize(d.sizeBytes)}</span>}
                  </div>
                )
              })}
            </div>
          ) : (
            <div className="secondary-body6 italic text-functional-text-light">
              Assign this node to a server to see its OS + data disks.
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
