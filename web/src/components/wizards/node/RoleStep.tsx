// Role selection + role interface mapping.
import { CosRadioButton, CosTooltip } from '@cube-frontend/ui-library'
import { NodeRole, NodeRoleSettings } from '../../../model/types'
import {
  createDefaultRoleSettings,
  nodeRoles,
  roleIFPrompts,
  roleOptions,
} from '../../../model/roles'
import { Select } from '../../form/Select'
import { allDraftIFs, IFDraft, isIFSelectable } from './nodeDraft'

export type RoleStepProps = {
  draft: IFDraft
  role: NodeRole
  roleSettings: NodeRoleSettings
  onRoleChange: (role: NodeRole, settings: NodeRoleSettings) => void
  onRoleSettingsChange: (settings: NodeRoleSettings) => void
}

export const RoleStep = (props: RoleStepProps) => {
  const { draft, role, roleSettings, onRoleChange, onRoleSettingsChange } =
    props

  return (
    <div className="flex flex-col gap-y-5">
      <div className="flex flex-col gap-y-2">
        <span className="primary-body2 font-semibold">Node role</span>
        <div className="flex flex-wrap gap-x-6 gap-y-2">
          {nodeRoles.map((r) => (
            <CosRadioButton
              key={r}
              name="node-role"
              label={r}
              checked={role === r}
              onChange={() =>
                onRoleChange(r, createDefaultRoleSettings(r, roleSettings))
              }
            />
          ))}
        </div>
      </div>
      <div className="flex flex-col gap-y-4">
        {roleOptions[role].map((key) => {
          const prompt = roleIFPrompts[key]
          const selectable = allDraftIFs(draft).filter((f) =>
            isIFSelectable(f, key),
          )
          return (
            <div key={key} className="flex items-center gap-x-2">
              <Select<string>
                label={prompt.label}
                className="w-64"
                value={roleSettings[key]?.id}
                options={selectable.map((f) => ({
                  value: f.id,
                  label: f.name,
                }))}
                onChange={(id) => {
                  const IF = selectable.find((f) => f.id === id)
                  onRoleSettingsChange({
                    ...roleSettings,
                    [key]: IF ? { id: IF.id, type: IF.type } : {},
                  })
                }}
              />
              <CosTooltip
                hoverContent={{ message: prompt.info }}
                placement="top-center"
              >
                <span className="secondary-body5 cursor-help text-functional-text-light">
                  ⓘ
                </span>
              </CosTooltip>
            </div>
          )
        })}
      </div>
    </div>
  )
}
