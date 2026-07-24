import { CosInlineNotification, CosModal } from '@cube-frontend/ui-library'
import { useState } from 'react'
import {
  importMachines,
  ImportResult,
  importTemplateUrl,
} from '../../api/machines'

export type ImportMachinesModalProps = {
  isOpen: boolean
  onCancel: () => void
  onDone: () => void
}

export const ImportMachinesModal = (props: ImportMachinesModalProps) => {
  const { isOpen, onCancel, onDone } = props
  const [file, setFile] = useState<File | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState<ImportResult | null>(null)

  if (!isOpen) return null

  const runImport = async () => {
    if (!file) return
    setBusy(true)
    setError('')
    setResult(null)
    try {
      const res = await importMachines(file)
      setResult(res)
      if (res.errors.length === 0) {
        onDone()
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <CosModal
      isOpen={isOpen}
      title="Import machines"
      size="sm"
      actionText="Import"
      actionButtonProps={{ disabled: !file || busy, loading: busy }}
      onActionClick={runImport}
      onCloseClick={onCancel}
    >
      <div className="flex flex-col gap-y-4">
        <p className="primary-body3">
          Upload an <strong>.xlsx</strong> or <strong>.csv</strong> with
          columns <code>label</code>, <code>bmc_address</code>,{' '}
          <code>bmc_username</code>, <code>bmc_password</code>.{' '}
          <a
            href={importTemplateUrl}
            className="text-primary underline"
            download
          >
            Download template
          </a>
        </p>
        <input
          type="file"
          accept=".xlsx,.csv"
          aria-label="import file"
          onChange={(e) => {
            setFile(e.target.files?.[0] ?? null)
            setResult(null)
          }}
        />
        {result && (
          <CosInlineNotification
            type={result.errors.length ? 'warning' : 'positive'}
            isClosable={false}
          >
            {`Imported ${result.created} machine(s)` +
              (result.errors.length
                ? `; ${result.errors.length} row(s) skipped: ` +
                  result.errors
                    .map((e) => `row ${e.row} (${e.message})`)
                    .join(', ')
                : '.')}
          </CosInlineNotification>
        )}
        {error && (
          <CosInlineNotification type="error" title="Import failed" isClosable={false}>
            {error}
          </CosInlineNotification>
        )}
      </div>
    </CosModal>
  )
}
