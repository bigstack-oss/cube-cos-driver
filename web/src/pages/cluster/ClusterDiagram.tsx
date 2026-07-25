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
type DevGlyph = { name: string; isBond: boolean; members: string[]; color: string }

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

// NIC glyph, tinted to its role/wire color so the port matches the line it feeds.
const Nic = ({ x, y, color }: { x: number; y: number; color: string }) => (
  <g style={{ pointerEvents: 'none' }}>
    <rect x={x - 11} y={y - 8} width={22} height={16} rx={2} fill={shade(color, 0.6)} stroke={color} strokeWidth={1.25} />
    <line x1={x - 4} y1={y - 6} x2={x - 4} y2={y + 6} stroke={color} strokeWidth={1} />
    <line x1={x + 4} y1={y - 6} x2={x + 4} y2={y + 6} stroke={color} strokeWidth={1} />
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
        const dev = c.bond?.name ?? c.baseIf?.name ?? c.roleIf?.name
        if (!dev) continue
        const b = busInfo.get(dev) ?? { isBond: !!c.bond, roles: new Set<RoleIFKey>() }
        b.isBond = b.isBond || !!c.bond
        b.roles.add(c.role)
        busInfo.set(dev, b)
      }
    }
    const names = [...busInfo.keys()]
    // Split planes: a plane rides the TOP bus only if every role it carries is
    // Management/Provider; any Overlay/Storage on it sends the whole plane DOWN.
    // So a shared Provider+Overlay+Storage bond goes down, while dedicated
    // mgmt/provider planes go up.
    const TOP_ROLES = new Set<RoleIFKey>(['mgmtIF', 'providerIF'])
    const isTopBus = (d: string) => [...busInfo.get(d)!.roles].every((r) => TOP_ROLES.has(r))
    let topNames = names.filter(isTopBus)
    let bottomNames = names.filter((d) => !isTopBus(d))
    // Never leave the top empty: a single combined NIC carrying every role (or any
    // config where no plane is pure mgmt/provider) promotes its mgmt-carrying
    // plane to the top — so a 1-line node reads "up".
    if (topNames.length === 0 && names.length > 0) {
      const promote = names.find((d) => busInfo.get(d)!.roles.has('mgmtIF')) ?? names[0]
      topNames = [promote]
      bottomNames = bottomNames.filter((d) => d !== promote)
    }
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
  const W = 1320
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
  const glyphGap = 38 // glyph labels are short now (bond0); detail is in the legend
  const n = groups.length
  const areaW = busX1 - busX0
  const gap = n > 1 ? Math.max(20, (areaW - n * bw) / (n + 1)) : (areaW - bw) / 2
  const step = bw + gap
  const startX = busX0 + gap

  // Per-device detail for the right-side legend (bond -> members -> role), so the
  // glyphs stay compact (just the device name).
  const devLegend = Object.values(buses)
    .sort((a, b) => a.y - b.y)
    .map((b) => {
      let members: string[] = []
      for (const nd of nodes) {
        const bond = nd.bondIFs.find((f) => f.name === b.device)
        if (bond?.slaves?.length) {
          members = bond.slaves
            .map((sid) => nd.initIFs.find((f) => f.id === sid)?.name)
            .filter((x): x is string => !!x)
          break
        }
      }
      return {
        name: b.device,
        isBond: b.isBond,
        members,
        roleName: b.roles.map((r) => ROLE[r].name).join(' / '),
        color: ROLE[b.roles[0]].color,
      }
    })

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
            <text x={busX0} y={b.y < 200 ? b.y - 14 : b.y + 24} fontSize={12.5} fontWeight={700} fill="currentColor">
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
              const dev = c.bond?.name ?? c.baseIf?.name ?? c.roleIf?.name
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
          const devOf = (c: RoleChain): DevGlyph | null => {
            const name = c.bond?.name ?? c.baseIf?.name ?? c.roleIf?.name
            if (!name) return null
            const members = (c.bond?.slaves ?? [])
              .map((sid) => grp.rep.initIFs.find((f) => f.id === sid)?.name)
              .filter((x): x is string => !!x)
            const primary = buses[name]?.roles[0] ?? c.role
            return { name, isBond: !!c.bond, members, color: ROLE[primary].color }
          }
          const uniqDevs = (ws: Wire[]): DevGlyph[] => {
            const m = new Map<string, DevGlyph>()
            for (const c of grp.chains) {
              if (!ws.some((w) => w.role === c.role)) continue
              const d = devOf(c)
              if (d) m.set(d.name, d)
            }
            return [...m.values()]
          }
          const topDevGlyphs = uniqDevs(topWires)
          const botDevGlyphs = uniqDevs(botWires)
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
              {topDevGlyphs.map((d, k) => {
                const gx = cx + (k - (topDevGlyphs.length - 1) / 2) * glyphGap
                return (
                  <g key={'t' + d.name}>
                    <Nic x={gx} y={boxTopY} color={d.color} />
                    <text x={gx} y={boxTopY - 8} fontSize={9.5} fontWeight={600} textAnchor="middle" fill="currentColor" opacity={0.9} style={{ fontFamily: 'monospace' }}>
                      {d.name}
                    </text>
                  </g>
                )
              })}
              {botDevGlyphs.map((d, k) => {
                const gx = cx + (k - (botDevGlyphs.length - 1) / 2) * glyphGap
                return (
                  <g key={'b' + d.name}>
                    <Nic x={gx} y={boxTopY + bh} color={d.color} />
                    <text x={gx} y={boxTopY + bh + 17} fontSize={9.5} fontWeight={600} textAnchor="middle" fill="currentColor" opacity={0.9} style={{ fontFamily: 'monospace' }}>
                      {d.name}
                    </text>
                  </g>
                )
              })}
              <text x={cx} y={boxTopY - 27} fontSize={12} textAnchor="middle" fill="currentColor" opacity={0.75}>
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
        </g>

        {/* ports & bonds legend: the NIC detail (LACP + members + role) lives here
            so the diagram glyphs stay compact (just bond0/1/2/3, IF.n). */}
        {devLegend.length > 0 && (
          <g transform={`translate(${legendX}, ${topY + 20 + ROLE_ORDER.length * 20 + 34})`}>
            <text x={0} y={0} fontSize={12} fontWeight={700} fill="currentColor">
              Ports &amp; bonds
            </text>
            {devLegend.map((d, i) => (
              <g key={d.name} transform={`translate(0, ${20 + i * 30})`}>
                <Nic x={11} y={2} color={d.color} />
                <text x={32} y={-1} fontSize={11} fontWeight={600} fill="currentColor" style={{ fontFamily: 'monospace' }}>
                  {d.name}
                  {d.isBond ? ' (LACP)' : ''}
                </text>
                <text x={32} y={11} fontSize={9.5} fill="currentColor" opacity={0.65} style={{ fontFamily: 'monospace' }}>
                  {d.members.length ? d.members.join(' + ') + ' · ' : ''}
                  {d.roleName}
                </text>
              </g>
            ))}
          </g>
        )}
      </svg>

      {selected && (
        <CosModal
          isOpen
          size="md"
          title={`${selected.role}${selected.nodes.length > 1 ? ` ×${selected.nodes.length}` : ''} — network topology`}
          actionText="Close"
          isCancelButtonVisible={false}
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
