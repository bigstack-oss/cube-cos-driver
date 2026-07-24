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
  onReorderPort: (index: number, dir: -1 | 1) => void
}

type Line = { x1: number; y1: number; x2: number; y2: number; label?: string }

const portIndexOf = (f: IF): number => {
  const m = /^IF\.(\d+)$/.exec(f.name)
  return m ? parseInt(m[1], 10) - 1 : -1
}

export const SnapshotNetworkMapper = (props: SnapshotNetworkMapperProps) => {
  const { node, ports, onChange, onReorderPort } = props
  const containerRef = useRef<HTMLDivElement>(null)
  const anchors = useRef<Map<string, HTMLElement>>(new Map())
  const [lines, setLines] = useState<Line[]>([])
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
      const out: Line[] = []
      // Real port -> base interface (existing binding, derived from label).
      bases.forEach((b) => {
        const idx = portIndexOf(b)
        if (idx < 0 || idx >= ports.length) return
        const a = point(`port-${idx}`, 'r')
        const bb = point(`base-${b.id}`, 'l')
        if (a && bb) out.push({ x1: a.x, y1: a.y, x2: bb.x, y2: bb.y })
      })
      // Base -> role (VLAN name shown at the midpoint when tagged).
      chains.forEach((ch) => {
        if (!ch.baseIf) return
        const a = point(`base-${ch.baseIf.id}`, 'r')
        const rr = point(`role-${ch.role}`, 'l')
        if (a && rr) out.push({ x1: a.x, y1: a.y, x2: rr.x, y2: rr.y, label: ch.vlan?.name })
      })
      setLines((prev) =>
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
        {lines.map((l, i) => (
          <g key={i}>
            <line
              x1={l.x1}
              y1={l.y1}
              x2={l.x2}
              y2={l.y2}
              className="stroke-primary"
              strokeWidth={1.5}
            />
            {l.label && (
              <text
                x={(l.x1 + l.x2) / 2}
                y={(l.y1 + l.y2) / 2 - 4}
                className="fill-functional-text-light"
                fontSize={10}
                textAnchor="middle"
              >
                {l.label}
              </text>
            )}
          </g>
        ))}
      </svg>

      {/* Left: real NICs from the BMC (IF Table). */}
      <div className="z-10 flex w-64 flex-col gap-y-2">
        <span className="primary-body3 font-semibold">From BMC — IF Table</span>
        <p className="secondary-body5 text-functional-text-light">
          Ordered as CubeCOS enumerates ethX (PCI order). Reorder if the box
          differs.
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
            <span className="flex flex-col">
              <button
                className="leading-none disabled:opacity-30"
                disabled={k === 0}
                onClick={() => onReorderPort(k, -1)}
                aria-label={`move IF.${k + 1} up`}
              >
                ↑
              </button>
              <button
                className="leading-none disabled:opacity-30"
                disabled={k === ports.length - 1}
                onClick={() => onReorderPort(k, 1)}
                aria-label={`move IF.${k + 1} down`}
              >
                ↓
              </button>
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
