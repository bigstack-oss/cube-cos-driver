// Drag-and-drop network mapper: real NICs (from BMC discovery, ordered to
// match CubeCOS's PCI-sorted IF.N labels) are dragged onto role buckets
// (management / provider / overlay / storage[/backend]); bonds and VLANs are
// built from them. Output feeds the node's interface topology + role tags.
import { CosButton } from '@cube-frontend/ui-library'
import { useMemo, useState } from 'react'
import { roleIFPrompts, roleOptions, RoleIFKey } from '../../../model/roles'
import { NIC } from '../../../model/machine'
import { IF, NodeRole } from '../../../model/types'
import {
  allDraftIFs,
  createBond,
  createVlan,
  IFDraft,
  removeIFs,
} from '../../../components/wizards/node/nodeDraft'
import { newId } from '../../../utils/random'

export type NicMapperValue = {
  draft: IFDraft
  roleTags: Partial<Record<RoleIFKey, string>> // role -> interface id
}

export type NicMapperProps = {
  role: NodeRole
  // Physical NICs from discovery, already ordered (index 0 => IF.1).
  ports: NIC[]
  value: NicMapperValue
  onChange: (value: NicMapperValue) => void
  onReorderPort: (index: number, dir: -1 | 1) => void
}

const annotate = (ports: NIC[], index: number): string => {
  const p = ports[index]
  if (!p) return ''
  return [p.name, p.mac, p.speedMbps ? `${p.speedMbps}Mbps` : '']
    .filter(Boolean)
    .join(' · ')
}

export const NicMapper = (props: NicMapperProps) => {
  const { role, ports, value, onChange, onReorderPort } = props
  const { draft, roleTags } = value
  const [selected, setSelected] = useState<string[]>([])

  // Draggable chips: bonds, vlans, and init IFs that aren't bond slaves.
  const chips = useMemo(
    () =>
      allDraftIFs(draft).filter(
        (f) => !(f.type === 'init' && f.master),
      ),
    [draft],
  )

  const setDraft = (next: IFDraft) => onChange({ draft: next, roleTags })
  const tag = (key: RoleIFKey, id: string | undefined) =>
    onChange({
      draft,
      roleTags: { ...roleTags, [key]: id },
    })

  const chipLabel = (f: IF): string => {
    // For a plain init IF, show its physical annotation.
    const idx = draft.initIFs.findIndex((x) => x.id === f.id)
    const ann = f.type === 'init' && idx >= 0 ? annotate(ports, idx) : ''
    return ann ? `${f.name} (${ann})` : f.name
  }

  const roleKeys = roleOptions[role]

  return (
    <div className="flex flex-col gap-y-5">
      {/* Physical port order (confirm / reorder to match PCI/IF.N). */}
      <div className="flex flex-col gap-y-2">
        <span className="primary-body3 font-semibold">
          Physical ports → IF labels
        </span>
        <p className="secondary-body5 text-functional-text-light">
          CubeCOS assigns IF.N by PCI order. Reorder to match the real box if
          the discovered order differs.
        </p>
        <ul className="flex flex-col gap-y-1">
          {ports.map((_p, i) => (
            <li
              key={i}
              className="secondary-body4 flex items-center gap-x-2 rounded border border-functional-border-divider px-2 py-1"
            >
              <span className="w-12 font-semibold">IF.{i + 1}</span>
              <span className="flex-1">{annotate(ports, i) || '(no data)'}</span>
              <button
                className="px-1 disabled:opacity-30"
                disabled={i === 0}
                onClick={() => onReorderPort(i, -1)}
                aria-label={`move IF.${i + 1} up`}
              >
                ↑
              </button>
              <button
                className="px-1 disabled:opacity-30"
                disabled={i === ports.length - 1}
                onClick={() => onReorderPort(i, 1)}
                aria-label={`move IF.${i + 1} down`}
              >
                ↓
              </button>
            </li>
          ))}
        </ul>
      </div>

      {/* Bond / VLAN builders. */}
      <div className="flex items-center gap-x-2">
        <CosButton
          type="secondary"
          size="sm"
          disabled={selected.length < 2}
          onClick={() => {
            setDraft(createBond(draft, selected, newId()))
            setSelected([])
          }}
        >
          Bond selected
        </CosButton>
        {selected.length === 1 &&
          (() => {
            const parent = allDraftIFs(draft).find((f) => f.id === selected[0])
            if (!parent || parent.type === 'vlan') return null
            return (
              <CosButton
                type="secondary"
                size="sm"
                onClick={() => {
                  setDraft(createVlan(draft, parent, newId()))
                  setSelected([])
                }}
              >
                Add VLAN on selected
              </CosButton>
            )
          })()}
      </div>

      {/* Draggable interface chips. */}
      <div className="flex flex-col gap-y-2">
        <span className="primary-body3 font-semibold">Interfaces</span>
        <div className="flex flex-wrap gap-2">
          {chips.map((f) => {
            const isSelected = selected.includes(f.id)
            return (
              <div
                key={f.id}
                draggable
                onDragStart={(e) => e.dataTransfer.setData('text/plain', f.id)}
                onClick={() =>
                  setSelected((prev) =>
                    prev.includes(f.id)
                      ? prev.filter((x) => x !== f.id)
                      : [...prev, f.id],
                  )
                }
                className={`primary-body4 cursor-grab select-none rounded-md border px-3 py-1.5 ${
                  isSelected
                    ? 'border-primary bg-primary-0'
                    : 'border-functional-border-divider bg-grey-0'
                }`}
              >
                {chipLabel(f)}
                {(f.type === 'bond' || f.type === 'vlan') && (
                  <button
                    className="ml-2 text-status-negative"
                    onClick={(e) => {
                      e.stopPropagation()
                      setDraft(removeIFs(draft, [f.id]))
                    }}
                    aria-label={`remove ${f.name}`}
                  >
                    ×
                  </button>
                )}
              </div>
            )
          })}
        </div>
      </div>

      {/* Role drop zones. */}
      <div className="grid grid-cols-2 gap-3">
        {roleKeys.map((key) => {
          const assignedId = roleTags[key]
          const assigned = allDraftIFs(draft).find((f) => f.id === assignedId)
          return (
            <div
              key={key}
              onDragOver={(e) => e.preventDefault()}
              onDrop={(e) => {
                e.preventDefault()
                const id = e.dataTransfer.getData('text/plain')
                if (id) tag(key, id)
              }}
              className="flex min-h-[64px] flex-col gap-y-1 rounded-md border border-dashed border-functional-border-divider p-2"
            >
              <span className="secondary-body5 text-functional-text-light">
                {roleIFPrompts[key].label}
              </span>
              {assigned ? (
                <div className="primary-body4 flex items-center justify-between rounded bg-primary-0 px-2 py-1">
                  <span>{assigned.name}</span>
                  <button
                    className="text-status-negative"
                    onClick={() => tag(key, undefined)}
                    aria-label={`clear ${roleIFPrompts[key].label}`}
                  >
                    ×
                  </button>
                </div>
              ) : (
                <span className="secondary-body5 text-functional-disable-text">
                  drag an interface here
                </span>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
