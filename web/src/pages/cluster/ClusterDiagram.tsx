// Isometric-style cluster network diagram for config review. Nodes are grouped
// by role into rounded boxes (e.g. "compute ×10"), each wired to horizontal
// bus-bars — one per physical NIC / bond plane — with role-colored wires
// carrying the VLAN + subnet each plane serves. Clicking a box drills into that
// role group's per-node detail (NodeTopology). Renders the whole topology at a
// glance so an operator can confirm it matches intent before deploy.
import { useMemo, useState } from 'react'
import { CosModal } from '@cube-frontend/ui-library'
import { ClusterConfig, NodeConfig } from '../../model/types'
import { Machine } from '../../model/machine'
import { RoleIFKey } from '../../model/roles'
import { buildChains, RoleChain } from './assign/snapshotTopology'
import { NodeTopology } from './NodeTopology'

type Props = {
  cluster: ClusterConfig
  nodes: NodeConfig[]
  machineByHostname: Record<string, Machine>
}

const ROLE: Record<RoleIFKey, { name: string; color: string }> = {
  mgmtIF: { name: 'Management', color: '#4C68F9' },
  providerIF: { name: 'Provider', color: '#2AA7D8' },
  overlayIF: { name: 'Overlay', color: '#00B98C' },
  storIF: { name: 'Storage', color: '#D98A1F' },
  storIFBackend: { name: 'Storage backend', color: '#9B426E' },
}
const ROLE_ORDER: RoleIFKey[] = ['mgmtIF', 'providerIF', 'overlayIF', 'storIF', 'storIFBackend']

// Box fill by node role — each cube role a distinct color.
const NODE_ROLE_COLOR: Record<string, string> = {
  control: '#14B8A6',
  'control-converged': '#6366F1',
  compute: '#3B82F6',
  storage: '#D97706',
  'edge-core': '#A855F7',
  moderator: '#64748B',
}
const nodeRoleColor = (role: string): string => NODE_ROLE_COLOR[role] ?? '#4C68F9'

const vlanTag = (name?: string): string => {
  if (!name) return ''
  const i = name.lastIndexOf('.')
  return i >= 0 ? name.slice(i + 1) : ''
}

const maskToPrefix = (mask?: string): number =>
  !mask
    ? 0
    : mask
        .split('.')
        .reduce((n, o) => n + ((parseInt(o, 10) >>> 0).toString(2).match(/1/g)?.length ?? 0), 0)

// network/prefix for a role plane (shared across a role group), e.g. 172.18.0.0/16.
const subnet = (ip?: string, mask?: string): string => {
  if (!ip || !mask) return ''
  const i = ip.split('.').map(Number)
  const m = mask.split('.').map(Number)
  if (i.length !== 4 || m.length !== 4) return ip
  return i.map((o, k) => o & m[k]).join('.') + '/' + maskToPrefix(mask)
}

// lighten (amt>0) / darken (amt<0) a #rrggbb toward white/black.
const shade = (hex: string, amt: number): string => {
  const n = parseInt(hex.slice(1), 16)
  const t = amt < 0 ? 0 : 255
  const p = Math.abs(amt)
  const r = Math.round(((n >> 16) & 255) * (1 - p) + t * p)
  const g = Math.round(((n >> 8) & 255) * (1 - p) + t * p)
  const b = Math.round((n & 255) * (1 - p) + t * p)
  return `#${((1 << 24) + (r << 16) + (g << 8) + b).toString(16).slice(1)}`
}

type Bus = { device: string; isBond: boolean; roles: RoleIFKey[]; y: number; color: string }
type Wire = { role: RoleIFKey; side: 'top' | 'bottom'; busY: number; net: string; vlan: string }
type Group = { role: string; nodes: NodeConfig[]; rep: NodeConfig; chains: RoleChain[] }

const Box = ({
  x,
  y,
  w,
  h,
  color,
  role,
  count,
  selected,
  onClick,
}: {
  x: number
  y: number
  w: number
  h: number
  color: string
  role: string
  count: number
  selected: boolean
  onClick: () => void
}) => (
  <g onClick={onClick} style={{ cursor: 'pointer' }} filter={selected ? 'url(#sel)' : undefined}>
    <rect x={x} y={y} width={w} height={h} rx={10} fill={color} stroke={shade(color, -0.18)} strokeWidth={1.5} />
    <text
      x={x + w / 2}
      y={y + h / 2 + (count > 1 ? -3 : 4)}
      fontSize={14}
      fontWeight={700}
      textAnchor="middle"
      fill="#FFFFFF"
      lengthAdjust="spacingAndGlyphs"
      textLength={role.length > 13 ? w - 20 : undefined}
      style={{ pointerEvents: 'none' }}
    >
      {role}
    </text>
    {count > 1 && (
      <text x={x + w / 2} y={y + h / 2 + 15} fontSize={13} fontWeight={800} textAnchor="middle" fill="#FFFFFF" opacity={0.9} style={{ pointerEvents: 'none' }}>
        ×{count}
      </text>
    )}
  </g>
)

// NIC glyph: grey for uplink (top), blue for data (bottom).
const Nic = ({ x, y, blue }: { x: number; y: number; blue: boolean }) => (
  <g style={{ pointerEvents: 'none' }}>
    <rect x={x - 11} y={y - 8} width={22} height={16} rx={2} fill={blue ? '#7FB2E8' : '#C9CDD6'} stroke="rgba(0,0,0,.25)" strokeWidth={0.5} />
    <line x1={x - 4} y1={y - 6} x2={x - 4} y2={y + 6} stroke="rgba(0,0,0,.3)" strokeWidth={1} />
    <line x1={x + 4} y1={y - 6} x2={x + 4} y2={y + 6} stroke="rgba(0,0,0,.3)" strokeWidth={1} />
  </g>
)

export const ClusterDiagram = (props: Props) => {
  const { cluster, nodes, machineByHostname } = props
  const [selected, setSelected] = useState<Group | undefined>()
  const [viewHost, setViewHost] = useState<string>('')

  const layout = useMemo(() => {
    // Small clusters (<=5 nodes): fan every node out as its own box. Larger:
    // group by role into one box per role ("compute ×10").
    let groups: Group[]
    if (nodes.length <= 5) {
      groups = nodes.map((n) => ({ role: n.role, nodes: [n], rep: n, chains: buildChains(n) }))
    } else {
      const byRole = new Map<string, NodeConfig[]>()
      for (const n of nodes) {
        const g = byRole.get(n.role) ?? []
        g.push(n)
        byRole.set(n.role, g)
      }
      groups = [...byRole.entries()].map(([role, ns]) => ({
        role,
        nodes: ns,
        rep: ns[0],
        chains: buildChains(ns[0]),
      }))
    }

    // Buses: distinct base NIC/bond planes across the cluster.
    const busInfo = new Map<string, { isBond: boolean; roles: Set<RoleIFKey> }>()
    for (const n of nodes) {
      for (const c of buildChains(n)) {
        const dev = c.baseIf?.name ?? c.bond?.name ?? c.roleIf?.name
        if (!dev) continue
        const b = busInfo.get(dev) ?? { isBond: !!c.bond, roles: new Set<RoleIFKey>() }
        b.isBond = b.isBond || !!c.bond
        b.roles.add(c.role)
        busInfo.set(dev, b)
      }
    }
    const names = [...busInfo.keys()]
    const topNames = names.filter((d) => busInfo.get(d)!.roles.has('mgmtIF'))
    const bottomNames = names.filter((d) => !busInfo.get(d)!.roles.has('mgmtIF'))
    const buses: Record<string, Bus> = {}
    topNames.forEach((d, i) => {
      buses[d] = { device: d, isBond: busInfo.get(d)!.isBond, roles: ROLE_ORDER.filter((r) => busInfo.get(d)!.roles.has(r)), y: 88 - i * 44, color: '#2C3454' }
    })
    bottomNames.forEach((d, i) => {
      buses[d] = { device: d, isBond: busInfo.get(d)!.isBond, roles: ROLE_ORDER.filter((r) => busInfo.get(d)!.roles.has(r)), y: 476 + i * 44, color: '#7C8AC4' }
    })
    return { groups, buses }
  }, [nodes])

  const { groups, buses } = layout
  const W = 1220
  const busX0 = 96
  const busX1 = 946
  const legendX = 972
  const busYs = Object.values(buses).map((b) => b.y)
  const topY = Math.min(...busYs, 88)
  const botY = Math.max(...busYs, 476)
  const H = botY + 72

  const bw = 156
  const bh = 120
  const boxTopY = 210
  const n = groups.length
  const span = busX1 - busX0 - 40
  const step = n > 1 ? Math.min(230, span / n) : 0
  const startX = busX0 + 60 + (span - step * (n - 1) - bw) / 2

  return (
    <div className="overflow-x-auto rounded-2xl border border-functional-border-divider bg-functional-background-default p-2 text-functional-text-primary">
      <svg viewBox={`0 0 ${W} ${H}`} width="100%" style={{ minWidth: 820 }}>
        <defs>
          <filter id="sel" x="-20%" y="-20%" width="140%" height="140%">
            <feDropShadow dx="0" dy="0" stdDeviation="6" floodColor="#4C68F9" floodOpacity="0.9" />
          </filter>
        </defs>

        {/* bus-bars */}
        {Object.values(buses).map((b) => (
          <g key={b.device}>
            <line x1={busX0} y1={b.y} x2={busX1} y2={b.y} stroke={b.color} strokeWidth={5} strokeLinecap="round" />
            <text x={busX0} y={b.y < 200 ? b.y - 27 : b.y + 20} fontSize={13} fontWeight={700} fill="currentColor">
              {b.device}
              {b.isBond ? ' · LACP' : ''}
            </text>
            <text x={busX0} y={b.y < 200 ? b.y - 12 : b.y + 35} fontSize={11} fill="currentColor" opacity={0.6}>
              {b.roles.map((r) => ROLE[r].name).join(' / ')}
            </text>
          </g>
        ))}

        {/* role-group boxes + wires */}
        {groups.map((grp, gi) => {
          const bx = startX + gi * step
          const cx = bx + bw / 2
          const wires: Wire[] = grp.chains
            .map((c): Wire | null => {
              const dev = c.baseIf?.name ?? c.bond?.name ?? c.roleIf?.name
              const bus = dev ? buses[dev] : undefined
              if (!bus) return null
              return {
                role: c.role,
                side: bus.y < 200 ? 'top' : 'bottom',
                busY: bus.y,
                net: subnet(c.ip, c.roleIf?.IPMask),
                vlan: vlanTag(c.vlan?.name),
              }
            })
            .filter((w): w is Wire => !!w)

          const topWires = wires.filter((w) => w.side === 'top')
          const botWires = wires.filter((w) => w.side === 'bottom')
          const topDevs = new Set(grp.chains.filter((c) => topWires.some((w) => w.role === c.role)).map((c) => c.baseIf?.name)).size || 1
          const botDevs = new Set(grp.chains.filter((c) => botWires.some((w) => w.role === c.role)).map((c) => c.baseIf?.name)).size || 1
          const topBusYNode = Math.min(...topWires.map((w) => w.busY), 88)
          const botBusYNode = Math.max(...botWires.map((w) => w.busY), 476)

          const drawWires = (ws: Wire[], toTop: boolean) =>
            ws.map((w, k) => {
              const wx = cx + (k - (ws.length - 1) / 2) * 16
              const nicY = toTop ? boxTopY : boxTopY + bh
              return (
                <g key={w.role}>
                  <path d={`M ${wx},${nicY} L ${wx},${w.busY}`} stroke={ROLE[w.role].color} strokeWidth={2.5} fill="none" />
                  <circle cx={wx} cy={w.busY} r={3.5} fill={ROLE[w.role].color} />
                </g>
              )
            })

          const drawLabels = (ws: Wire[], toTop: boolean) =>
            ws
              .filter((w) => w.net || w.vlan)
              .map((w, i) => {
                const txt = `${w.vlan ? `vlan${w.vlan} ` : ''}${w.net}`
                const ly = toTop ? topBusYNode + 18 + i * 15 : botBusYNode - 16 - i * 15
                const cw = txt.length * 6 + 10
                return (
                  <g key={'l' + w.role}>
                    <rect x={cx - cw / 2} y={ly - 10} width={cw} height={15} rx={3} fill="rgba(255,255,255,.92)" stroke={ROLE[w.role].color} strokeWidth={0.75} />
                    <text x={cx} y={ly + 1} fontSize={9.5} textAnchor="middle" fill="#1B2137">
                      {txt}
                    </text>
                  </g>
                )
              })

          return (
            <g key={grp.rep.id}>
              {drawWires(topWires, true)}
              {drawWires(botWires, false)}
              <Box
                x={bx}
                y={boxTopY}
                w={bw}
                h={bh}
                color={nodeRoleColor(grp.role)}
                role={grp.role}
                count={grp.nodes.length}
                selected={selected?.rep.id === grp.rep.id}
                onClick={() => {
                  setSelected(grp)
                  setViewHost(grp.rep.hostname)
                }}
              />
              {Array.from({ length: topDevs }).map((_, k) => (
                <Nic key={'t' + k} x={cx + (k - (topDevs - 1) / 2) * 26} y={boxTopY} blue={false} />
              ))}
              {Array.from({ length: botDevs }).map((_, k) => (
                <Nic key={'b' + k} x={cx + (k - (botDevs - 1) / 2) * 26} y={boxTopY + bh} blue={true} />
              ))}
              <text x={cx} y={boxTopY - 12} fontSize={12} textAnchor="middle" fill="currentColor" opacity={0.75}>
                {grp.nodes.length === 1 ? grp.nodes[0].hostname : `${grp.nodes.length} nodes`}
              </text>
              {drawLabels(topWires, true)}
              {drawLabels(botWires, false)}
            </g>
          )
        })}

        {/* legend */}
        <g transform={`translate(${legendX}, ${topY + 20})`}>
          <text x={0} y={0} fontSize={12} fontWeight={700} fill="currentColor">
            Network roles
          </text>
          {ROLE_ORDER.map((r, i) => (
            <g key={r} transform={`translate(0, ${16 + i * 20})`}>
              <line x1={0} y1={0} x2={22} y2={0} stroke={ROLE[r].color} strokeWidth={3} />
              <circle cx={22} cy={0} r={3} fill={ROLE[r].color} />
              <text x={32} y={4} fontSize={11} fill="currentColor">
                {ROLE[r].name}
              </text>
            </g>
          ))}
          <g transform={`translate(0, ${16 + ROLE_ORDER.length * 20 + 10})`}>
            <Nic x={11} y={0} blue={false} />
            <text x={32} y={4} fontSize={11} fill="currentColor">
              Uplink NIC
            </text>
            <g transform="translate(0, 24)">
              <Nic x={11} y={0} blue={true} />
              <text x={32} y={4} fontSize={11} fill="currentColor">
                Data NIC
              </text>
            </g>
          </g>
        </g>
      </svg>

      {selected && (
        <CosModal
          isOpen
          size="md"
          title={`${selected.role}${selected.nodes.length > 1 ? ` ×${selected.nodes.length}` : ''} — network topology`}
          actionText="Close"
          onActionClick={() => setSelected(undefined)}
          onCloseClick={() => setSelected(undefined)}
        >
          {(() => {
            const viewNode = selected.nodes.find((n) => n.hostname === viewHost) ?? selected.rep
            return (
              <div className="flex flex-col gap-y-3">
                {selected.nodes.length > 1 && (
                  <div className="flex flex-wrap gap-2">
                    {selected.nodes.map((nd) => {
                      const active = nd.hostname === viewNode.hostname
                      return (
                        <button
                          key={nd.id}
                          onClick={() => setViewHost(nd.hostname)}
                          className={
                            'rounded-md border px-2.5 py-1 secondary-body6 font-mono transition-colors ' +
                            (active
                              ? 'border-primary-hover bg-primary-hover/10 font-semibold text-primary-hover'
                              : 'border-functional-border-divider text-functional-text-secondary hover:bg-functional-background-hover')
                          }
                        >
                          {nd.hostname}
                        </button>
                      )
                    })}
                  </div>
                )}
                <NodeTopology
                  node={viewNode}
                  cluster={cluster}
                  machine={machineByHostname[viewNode.hostname]}
                />
              </div>
            )
          })()}
        </CosModal>
      )}
    </div>
  )
}
