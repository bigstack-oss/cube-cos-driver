// Import a previously exported clusterDetail.json into the local drafts.
import { CosInlineNotification, CosModal } from '@cube-frontend/ui-library'
import { useState } from 'react'
import { checkClusterDetailValid } from '../../model/importCluster'
import { ClusterDetail } from '../../model/types'

export type ImportModalProps = {
  isOpen: boolean
  onCancel: () => void
  onImport: (detail: ClusterDetail) => void
}

export const ImportModal = (props: ImportModalProps) => {
  const { isOpen, onCancel, onImport } = props
  const [error, setError] = useState('')
  const [detail, setDetail] = useState<ClusterDetail | null>(null)
  const [fileName, setFileName] = useState('')

  if (!isOpen) return null

  const handleFile = async (file: File | undefined) => {
    setError('')
    setDetail(null)
    if (!file) return
    setFileName(file.name)
    try {
      const parsed: unknown = JSON.parse(await file.text())
      const result = checkClusterDetailValid(parsed)
      if (!result.ok) {
        setError(result.message)
        return
      }
      setDetail(result.detail)
    } catch {
      setError('The file is not valid JSON.')
    }
  }

  return (
    <CosModal
      isOpen={isOpen}
      title="Import cluster"
      size="sm"
      actionText="Import"
      actionButtonProps={{ disabled: !detail }}
      onActionClick={() => {
        if (detail) onImport(detail)
      }}
      onCloseClick={onCancel}
    >
      <div className="flex flex-col gap-y-4">
        <p className="primary-body3">
          Select a previously exported <code>clusterDetail.json</code>.
        </p>
        <input
          type="file"
          accept="application/json,.json"
          aria-label="clusterDetail.json file"
          onChange={(e) => void handleFile(e.target.files?.[0])}
        />
        {fileName && detail && (
          <CosInlineNotification type="positive" isClosable={false}>
            {`${fileName}: cluster "${detail.clusterInfo.name}" with ${detail.nodeData.length} node(s) ready to import.`}
          </CosInlineNotification>
        )}
        {error && (
          <CosInlineNotification
            type="error"
            title="Invalid file"
            isClosable={false}
          >
            {error}
          </CosInlineNotification>
        )}
      </div>
    </CosModal>
  )
}
