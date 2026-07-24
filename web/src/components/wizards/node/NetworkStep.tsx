// Interface editor: init/bond/VLAN tables with enable/IP/mask editing, bond
// and VLAN creation, plus default interface + gateway selection.
import {
  CosButton,
  CosCheckbox,
  CosInput,
  CosToggle,
} from '@cube-frontend/ui-library'
import { useState } from 'react'
import { IF } from '../../../model/types'
import { isValidIPv4 } from '../../../model/validate'
import { newId } from '../../../utils/random'
import { Select } from '../../form/Select'
import {
  allDraftIFs,
  createBond,
  createVlan,
  IFDraft,
  isIFSelectable,
  removeIFs,
  resizeInitIFs,
} from './nodeDraft'

export type NetworkStepProps = {
  draft: IFDraft
  defaultIFId: string | undefined
  defaultGateway: string
  onDraftChange: (next: IFDraft) => void
  onIFChange: (target: IF, patch: Partial<IF>) => void
  onDefaultIFChange: (IF: IF) => void
  onDefaultGatewayChange: (gateway: string) => void
}

const IFRow = (props: {
  IF: IF
  draft: IFDraft
  selected: boolean
  onSelect?: (checked: boolean) => void
  onIFChange: NetworkStepProps['onIFChange']
  onRemove?: () => void
}) => {
  const { IF, draft, selected, onSelect, onIFChange, onRemove } = props
  const isSlave = IF.type === 'init' && !!IF.master
  const masterName = allDraftIFs(draft).find((f) => f.id === IF.master)?.name
  return (
    <div className="flex flex-wrap items-end gap-x-3 gap-y-1 rounded-md border border-functional-border-divider p-3">
      {onSelect && (
        <CosCheckbox
          checked={selected}
          onChange={(e) => onSelect(e.target.checked)}
          disabled={isSlave}
        />
      )}
      <div className="w-24">
        <span className="primary-body3 font-semibold">{IF.name}</span>
        {IF.type !== 'init' && (
          <span className="secondary-body5 block text-functional-text-light">
            {IF.type}
            {IF.type === 'vlan' && masterName ? ` on ${masterName}` : ''}
          </span>
        )}
        {isSlave && (
          <span className="secondary-body5 block text-functional-text-light">
            slave of {masterName}
          </span>
        )}
      </div>
      <CosToggle
        label="Enabled"
        isOn={IF.enabled}
        disabled={isSlave}
        onChange={(on) => onIFChange(IF, { enabled: on })}
      />
      {IF.enabled && !isSlave && (
        <>
          <CosInput
            label="IP address"
            className="w-40"
            value={IF.IPAddr ?? ''}
            onChange={(e) => onIFChange(IF, { IPAddr: e.target.value })}
            errorMessage={
              IF.IPAddr && !isValidIPv4(IF.IPAddr) ? 'Invalid IP' : undefined
            }
          />
          <CosInput
            label="Netmask"
            className="w-40"
            value={IF.IPMask ?? ''}
            onChange={(e) => onIFChange(IF, { IPMask: e.target.value })}
            errorMessage={
              IF.IPMask && !isValidIPv4(IF.IPMask) ? 'Invalid mask' : undefined
            }
          />
        </>
      )}
      {onRemove && (
        <CosButton type="ghost" size="sm" onClick={onRemove}>
          Remove
        </CosButton>
      )}
    </div>
  )
}

export const NetworkStep = (props: NetworkStepProps) => {
  const {
    draft,
    defaultIFId,
    defaultGateway,
    onDraftChange,
    onIFChange,
    onDefaultIFChange,
    onDefaultGatewayChange,
  } = props
  const [selectedInit, setSelectedInit] = useState<string[]>([])

  const selectableDefaults = allDraftIFs(draft).filter((f) =>
    isIFSelectable(f, 'defaultIF'),
  )
  const vlanParents = allDraftIFs(draft).filter(
    (f) => f.enabled && f.type !== 'vlan' && !(f.type === 'init' && f.master),
  )
  const [vlanParentId, setVlanParentId] = useState<string | undefined>()

  return (
    <div className="flex flex-col gap-y-5">
      <div className="flex items-center gap-x-4">
        <span className="primary-body2 font-semibold">
          Physical interfaces ({draft.initIFs.length})
        </span>
        <CosButton
          type="secondary"
          size="sm"
          onClick={() =>
            onDraftChange(resizeInitIFs(draft, draft.initIFs.length + 1, newId))
          }
        >
          Add interface
        </CosButton>
        <CosButton
          type="secondary"
          size="sm"
          disabled={selectedInit.length < 2}
          onClick={() => {
            onDraftChange(createBond(draft, selectedInit, newId()))
            setSelectedInit([])
          }}
        >
          Bond selected
        </CosButton>
      </div>
      {draft.initIFs.map((IF, i) => (
        <IFRow
          key={IF.id}
          IF={IF}
          draft={draft}
          selected={selectedInit.includes(IF.id)}
          onSelect={(checked) =>
            setSelectedInit((prev) =>
              checked ? [...prev, IF.id] : prev.filter((id) => id !== IF.id),
            )
          }
          onIFChange={onIFChange}
          onRemove={
            i === draft.initIFs.length - 1 && draft.initIFs.length > 1
              ? () => onDraftChange(removeIFs(draft, [IF.id]))
              : undefined
          }
        />
      ))}

      {draft.bondIFs.length > 0 && (
        <span className="primary-body2 font-semibold">Bonds</span>
      )}
      {draft.bondIFs.map((IF) => (
        <IFRow
          key={IF.id}
          IF={IF}
          draft={draft}
          selected={false}
          onIFChange={onIFChange}
          onRemove={() => onDraftChange(removeIFs(draft, [IF.id]))}
        />
      ))}

      <div className="flex items-end gap-x-3">
        <Select<string>
          label="VLAN parent"
          className="w-56"
          value={vlanParentId}
          options={vlanParents.map((f) => ({ value: f.id, label: f.name }))}
          onChange={setVlanParentId}
        />
        <CosButton
          type="secondary"
          size="sm"
          disabled={!vlanParentId}
          onClick={() => {
            const parent = vlanParents.find((f) => f.id === vlanParentId)
            if (parent) onDraftChange(createVlan(draft, parent, newId()))
          }}
        >
          Add VLAN
        </CosButton>
      </div>
      {draft.vlanIFs.map((IF) => (
        <IFRow
          key={IF.id}
          IF={IF}
          draft={draft}
          selected={false}
          onIFChange={onIFChange}
          onRemove={() => onDraftChange(removeIFs(draft, [IF.id]))}
        />
      ))}

      <div className="flex items-end gap-x-3">
        <Select<string>
          label="Default interface (requires an enabled interface with IP)"
          className="w-56"
          value={defaultIFId}
          options={selectableDefaults.map((f) => ({
            value: f.id,
            label: f.name,
          }))}
          onChange={(id) => {
            const IF = selectableDefaults.find((f) => f.id === id)
            if (IF) onDefaultIFChange(IF)
          }}
        />
        <CosInput
          label="Default gateway"
          className="w-40"
          value={defaultGateway}
          onChange={(e) => onDefaultGatewayChange(e.target.value)}
          errorMessage={
            !isValidIPv4(defaultGateway) ? 'Invalid IP' : undefined
          }
        />
      </div>
    </div>
  )
}
