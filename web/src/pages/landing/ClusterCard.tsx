import {
  CosButton,
  CosGeneralPanel,
  CosInput,
  CosTag,
} from '@cube-frontend/ui-library'
import { useState } from 'react'
import { NodeConfig } from '../../model/types'
import { sanitizeClusterName } from '../../model/validate'

export type ClusterCardProps = {
  name: string
  nodes: NodeConfig[]
  serverOnly?: boolean
  fallbackName: string
  onOpen: () => void
  onRename?: (name: string) => void
  onExport?: () => void
  onDelete: () => void
}

export const ClusterCard = (props: ClusterCardProps) => {
  const {
    name,
    nodes,
    serverOnly,
    fallbackName,
    onOpen,
    onRename,
    onExport,
    onDelete,
  } = props
  const [editing, setEditing] = useState(false)
  const [draftName, setDraftName] = useState(name)

  const roleCounts = nodes.reduce<Record<string, number>>((acc, n) => {
    acc[n.role] = (acc[n.role] ?? 0) + 1
    return acc
  }, {})

  return (
    <CosGeneralPanel
      topic={editing ? undefined : name}
      subtext={`${nodes.length} node(s)`}
      rightSlot={
        serverOnly ? (
          <CosTag variant="stroke" color="cyan">
            server
          </CosTag>
        ) : undefined
      }
      className="w-80"
    >
      <div className="flex flex-col gap-y-3">
        {editing && onRename && (
          <CosInput
            value={draftName}
            aria-label="Cluster name"
            onChange={(e) => setDraftName(e.target.value)}
            onBlur={() => {
              onRename(sanitizeClusterName(draftName, fallbackName))
              setEditing(false)
            }}
          />
        )}
        <div className="flex flex-wrap gap-1">
          {Object.entries(roleCounts).map(([role, count]) => (
            <CosTag key={role} variant="stroke" color="primary-blue">
              {`${role} × ${count}`}
            </CosTag>
          ))}
        </div>
        <div className="flex flex-wrap gap-x-1">
          <CosButton size="sm" onClick={onOpen}>
            Open
          </CosButton>
          {onRename && !editing && (
            <CosButton
              size="sm"
              type="ghost"
              onClick={() => {
                setDraftName(name)
                setEditing(true)
              }}
            >
              Rename
            </CosButton>
          )}
          {onExport && (
            <CosButton size="sm" type="ghost" onClick={onExport}>
              Export
            </CosButton>
          )}
          <CosButton size="sm" type="warning" onClick={onDelete}>
            Delete
          </CosButton>
        </div>
      </div>
    </CosGeneralPanel>
  )
}
