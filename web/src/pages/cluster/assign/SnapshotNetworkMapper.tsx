// Connector-line binding view. Left = the machine's real NICs (IF Table,
// ordered to match CubeCOS PCI/ethX enumeration). Right = the snapshot's
// logical topology (base interface -> optional VLAN -> role), read-only,
// drawn with connector lines and each role's IP. Dragging a real NIC onto a
// base interface corrects which IF.N the topology uses (e.g. IF.4 -> IF.5),
// rewriting the snapshot's interface labels.
import { useLayoutEffect, useRef, useState } from 'react'
import { roleIFPrompts } from '../../../model/roles'
import { NIC } from '../../../model/machine'
import { IF, NodeConfig } from '../../../model/types'
import { baseInterfaces, bindPort, buildChains } from './snapshotTopology'

export type SnapshotNetworkMapperProps = {
  node: NodeConfig
  ports: NIC[]
  onChange: (node: NodeConfig) => void
}

type Vert = { x: number; y0: number; y1: number }
type Path = { d: string; color: string; label?: string; lx: number; ly: number }

const HOP = 6 // hump radius where a horizontal crosses another line's vertical

// Distinct categorical line colors from the cube theme.
const LINE_COLORS = [
  '#4C68F9', // primary
  '#00D5A2', // green
  '#31C4FF', // blue
  '#F9C300', // yellow
  '#FF5D5D', // red
  '#57E2E2', // cyan
]
const PORT_COLOR = '#8A8D97' // grey-300, for port->base bindings

// hHops draws a left-to-right horizontal segment at y from xa to xb, arcing a
// small upward "hump" over any vertical in `verts` it crosses (excluding the
// line's own channels in `own`).
const hHops = (y: number, xa: number, xb: number, verts: Vert[], own: number[]): string => {
  const crosses = verts
    .filter(
      (v) =>
        !own.some((x) => Math.abs(x - v.x) < 0.5) &&
        v.x > xa + HOP &&
        v.x < xb - HOP &&
        y > Math.min(v.y0, v.y1) + 0.5 &&
        y < Math.max(v.y0, v.y1) - 0.5,
    )
    .map((v) => v.x)
    .sort((a, b) => a - b)
  let d = ''
  for (const c of crosses) {
    d += ` L ${c - HOP} ${y} A ${HOP} ${HOP} 0 0 0 ${c + HOP} ${y}` // sweep 0 => hump up
  }
  return d + ` L ${xb} ${y}`
}

const portIndexOf = (f: IF): number => {
  const m = /^IF\.(\d+)$/.exec(f.name)
  return m ? parseInt(m[1], 10) - 1 : -1
}

export const SnapshotNetworkMapper = (props: SnapshotNetworkMapperProps) => {
  const { node, ports, onChange } = props
  const containerRef = useRef<HTMLDivElement>(null)
  const anchors = useRef<Map<string, HTMLElement>>(new Map())
  const [paths, setPaths] = useState<Path[]>([])
  const [dragIndex, setDragIndex] = useState<number | null>(null)

  const chains = buildChains(node)
  const bases = baseInterfaces(node)

  const setAnchor = (key: string) => (el: HTMLElement | null) => {
    if (el) anchors.current.set(key, el)
    else anchors.current.delete(key)
  }

  useLayoutEffect(() => {
    const measure = () => {
      const c = containerRef.current
      if (!c) return
      const o = c.getBoundingClientRect()
      const point = (key: string, side: 'l' | 'r') => {
        const el = anchors.current.get(key)
        if (!el) return null
        const r = el.getBoundingClientRect()
        return {
          x: (side === 'l' ? r.left : r.right) - o.left,
          y: r.top + r.height / 2 - o.top,
        }
      }
      // Collect base -> role connections (each drawn as its own bus route).
      type Conn = { x1: number; y1: number; x2: number; y2: number; label?: string }
      const conns: Conn[] = []
      chains.forEach((ch) => {
        if (!ch.baseIf) return
        const a = point(`base-${ch.baseIf.id}`, 'r')
        const rr = point(`role-${ch.role}`, 'l')
        if (a && rr) conns.push({ x1: a.x, y1: a.y, x2: rr.x, y2: rr.y, label: ch.vlan?.name })
      })

      const n = conns.length
      const baseRight = n ? Math.max(...conns.map((c) => c.x1)) : 0
      const roleLeft = n ? Math.min(...conns.map((c) => c.x2)) : 0
      const topY = n ? Math.min(...conns.flatMap((c) => [c.y1, c.y2])) : 0
      const botY = n ? Math.max(...conns.flatMap((c) => [c.y1, c.y2])) : 0

      // Each connection gets its own entry channel, horizontal lane, and exit
      // channel so no two lines share a run.
      const geo = conns.map((c, i) => ({
        ...c,
        entryX: baseRight + 10 + i * 7,
        exitX: roleLeft - 10 - i * 7,
        laneY: topY + ((i + 1) * (botY - topY)) / (n + 1),
      }))

      // Verticals from every connection (for hop detection).
      const verts: Vert[] = geo.flatMap((g) => [
        { x: g.entryX, y0: g.y1, y1: g.laneY },
        { x: g.exitX, y0: g.laneY, y1: g.y2 },
      ])

      const out: Path[] = []

      // Port -> base bindings: simple grey elbow on the left.
      bases.forEach((b) => {
        const idx = portIndexOf(b)
        if (idx < 0 || idx >= ports.length) return
        const a = point(`port-${idx}`, 'r')
        const bb = point(`base-${b.id}`, 'l')
        if (!a || !bb) return
        const mid = (a.x + bb.x) / 2
        out.push({
          d: `M ${a.x} ${a.y} L ${mid} ${a.y} L ${mid} ${bb.y} L ${bb.x} ${bb.y}`,
          color: PORT_COLOR,
          lx: 0,
          ly: 0,
        })
      })

      // Base -> role bus routes: stub → entry channel → lane → exit channel → stub.
      geo.forEach((g, i) => {
        const own = [g.entryX, g.exitX]
        const d =
          `M ${g.x1} ${g.y1}` +
          hHops(g.y1, g.x1, g.entryX, verts, own) +
          ` L ${g.entryX} ${g.laneY}` +
          hHops(g.laneY, g.entryX, g.exitX, verts, own) +
          ` L ${g.exitX} ${g.y2}` +
          hHops(g.y2, g.exitX, g.x2, verts, own)
        out.push({
          d,
          color: LINE_COLORS[i % LINE_COLORS.length],
          label: g.label,
          lx: g.entryX + 3,
          ly: g.laneY - 4,
        })
      })

      setPaths((prev) =>
        JSON.stringify(prev) === JSON.stringify(out) ? prev : out,
      )
    }
    measure()
    window.addEventListener('resize', measure)
    return () => window.removeEventListener('resize', measure)
    // Re-measure when the topology or port order changes.
  }, [node, ports])

  return (
    <div ref={containerRef} className="relative flex justify-between gap-x-4">
      <svg className="pointer-events-none absolute inset-0 h-full w-full overflow-visible">
        {paths.map((p, i) => (
          <g key={i}>
            <path d={p.d} stroke={p.color} strokeWidth={1.5} fill="none" />
            {p.label && (
              <text x={p.lx} y={p.ly} fill={p.color} fontSize={10}>
                {p.label}
              </text>
            )}
          </g>
        ))}
      </svg>

      {/* Left: real NICs from the BMC (IF Table). */}
      <div className="z-10 flex w-64 flex-col gap-y-2">
        <span className="primary-body3 font-semibold">From BMC — IF Table</span>
        <p className="secondary-body5 text-functional-text-light">
          Real NICs in CubeCOS ethX (PCI) order. Drag one onto a snapshot
          interface to bind/correct its IF.N.
        </p>
        {ports.map((p, k) => (
          <div
            key={k}
            ref={setAnchor(`port-${k}`)}
            draggable
            onDragStart={(e) => {
              e.dataTransfer.setData('text/plain', String(k))
              setDragIndex(k)
            }}
            onDragEnd={() => setDragIndex(null)}
            className="secondary-body4 flex cursor-grab items-center gap-x-2 rounded-md border border-functional-border-divider bg-grey-0 px-2 py-1.5"
          >
            <span className="w-10 font-semibold">IF.{k + 1}</span>
            <span className="flex-1 truncate">
              {[p.name, p.mac, p.speedMbps ? `${p.speedMbps}Mbps` : '']
                .filter(Boolean)
                .join(' · ') || '(no data)'}
            </span>
          </div>
        ))}
      </div>

      {/* Middle: base interfaces from the snapshot (drop targets). */}
      <div className="z-10 flex w-40 flex-col gap-y-3 self-center">
        <span className="primary-body3 font-semibold">From snapshot</span>
        {bases.map((b) => {
          const idx = portIndexOf(b)
          const bound = idx >= 0 && idx < ports.length ? ports[idx] : undefined
          return (
            <div
              key={b.id}
              ref={setAnchor(`base-${b.id}`)}
              onDragOver={(e) => e.preventDefault()}
              onDrop={(e) => {
                e.preventDefault()
                const raw = e.dataTransfer.getData('text/plain')
                const k = parseInt(raw, 10)
                if (!Number.isNaN(k)) onChange(bindPort(node, b.id, k))
                setDragIndex(null)
              }}
              className={`rounded-md border-2 px-2 py-2 ${
                dragIndex !== null
                  ? 'border-dashed border-primary'
                  : 'border-functional-border-divider'
              }`}
            >
              <div className="primary-body4 font-semibold">{b.name}</div>
              <div className="secondary-body5 text-functional-text-light">
                {bound ? (bound.mac ?? bound.name ?? 'bound') : 'drag a NIC here'}
              </div>
            </div>
          )
        })}
      </div>

      {/* Right: role boxes with IP. */}
      <div className="z-10 flex w-52 flex-col gap-y-3">
        <span className="primary-body3 font-semibold">Roles</span>
        {chains.map((ch) => (
          <div
            key={ch.role}
            ref={setAnchor(`role-${ch.role}`)}
            className="rounded-md border border-functional-border-divider px-2 py-2"
          >
            <div className="primary-body4 font-semibold">
              {roleIFPrompts[ch.role].label}
            </div>
            <div className="secondary-body5 text-functional-text-light">
              {ch.ip ? `IP ${ch.ip}` : 'no IP'}
              {ch.vlan ? ` · ${ch.vlan.name}` : ''}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
