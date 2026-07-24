import { CosButton, CosTag, GetCosBasicTable } from '@cube-frontend/ui-library'
import { useCallback, useEffect, useRef, useState } from 'react'
import {
  createMachine,
  deleteMachine,
  fetchMachineHardware,
  listMachines,
  updateMachine,
} from '../../api/machines'
import { formatBytes, Machine, MachineInput } from '../../model/machine'
import { ImportMachinesModal } from './ImportMachinesModal'
import { MachineDetails } from './MachineDetails'
import { MachineModal } from './MachineModal'

const Table = GetCosBasicTable<MachineRow>()

type MachineRow = {
  id: string
  label: string
  address: string
  serial: string
  cpu: string
  memory: string
  nics: number
  disks: number
  state: Machine['fetchState']
  machine: Machine
}

const fetchTag: Record<Machine['fetchState'], { color: 'default' | 'primary-blue' | 'cyan' | 'dark'; text: string }> = {
  idle: { color: 'default', text: 'not fetched' },
  fetching: { color: 'primary-blue', text: 'fetching…' },
  ok: { color: 'cyan', text: 'ok' },
  error: { color: 'dark', text: 'error' },
}

const toRow = (m: Machine): MachineRow => ({
  id: m.id,
  label: m.label,
  address: m.bmc.address,
  serial: m.inventory?.serial ?? '—',
  cpu: m.inventory?.cpuModel
    ? `${m.inventory.cpuModel} ×${m.inventory.cpuCount ?? 1}`
    : m.inventory?.cpuCount
      ? `×${m.inventory.cpuCount}`
      : '—',
  memory: formatBytes(m.inventory?.memoryBytes),
  nics: m.inventory?.nics?.length ?? 0,
  disks: m.inventory?.disks?.length ?? 0,
  state: m.fetchState,
  machine: m,
})

export const HardwarePage = () => {
  const [machines, setMachines] = useState<Machine[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [modalOpen, setModalOpen] = useState(false)
  const [editing, setEditing] = useState<Machine | undefined>()
  const [saving, setSaving] = useState(false)
  const [details, setDetails] = useState<Machine | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const refresh = useCallback(async () => {
    try {
      const list = await listMachines()
      setMachines(list)
      setError('')
      return list
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      return []
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    refresh()
  }, [refresh])

  // Poll while any machine is fetching.
  useEffect(() => {
    const anyFetching = machines.some((m) => m.fetchState === 'fetching')
    if (anyFetching && !pollRef.current) {
      pollRef.current = setInterval(refresh, 1500)
    } else if (!anyFetching && pollRef.current) {
      clearInterval(pollRef.current)
      pollRef.current = null
    }
    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current)
        pollRef.current = null
      }
    }
  }, [machines, refresh])

  const handleSave = async (input: MachineInput) => {
    setSaving(true)
    try {
      if (editing) {
        await updateMachine(editing.id, input)
      } else {
        await createMachine(input)
      }
      setModalOpen(false)
      setEditing(undefined)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setSaving(false)
    }
  }

  const handleFetch = async (m: Machine) => {
    try {
      await fetchMachineHardware(m.id)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const handleDelete = async (m: Machine) => {
    try {
      await deleteMachine(m.id)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const rows = machines.map(toRow)

  return (
    <div className="flex flex-col gap-y-6 p-8">
      <div className="flex flex-col gap-y-2">
        <h1 className="primary-h2">Hardware inventory</h1>
        <p className="primary-body2">
          Register machines by their BMC (IPMI/Redfish) and fetch hardware
          facts. Used later to appoint snapshots and drive zero-touch install.
        </p>
      </div>

      <div className="flex items-center gap-x-3">
        <CosButton
          onClick={() => {
            setEditing(undefined)
            setModalOpen(true)
          }}
        >
          Add machine
        </CosButton>
        <CosButton type="secondary" onClick={() => setImportOpen(true)}>
          Import from file
        </CosButton>
        {error && (
          <span className="primary-body4 text-status-negative">{error}</span>
        )}
      </div>

      <Table rows={rows} isLoading={loading}>
        <Table.Column label="Label" property="label" emphasize />
        <Table.Column label="BMC address" property="address" />
        <Table.Column label="Serial" property="serial" />
        <Table.Column label="CPU" property="cpu" />
        <Table.Column label="Memory" property="memory" />
        <Table.Column label="NICs" property="nics" />
        <Table.Column label="Disks" property="disks" />
        <Table.Column label="Fetch" property="state">
          {(state: Machine['fetchState']) => (
            <CosTag variant="stroke" color={fetchTag[state].color}>
              {fetchTag[state].text}
            </CosTag>
          )}
        </Table.Column>
        <Table.Column label="Actions" property="id" fitContent>
          {(_: string, row: MachineRow) => (
            <div className="flex gap-x-1">
              <CosButton
                type="ghost"
                size="sm"
                disabled={row.state === 'fetching'}
                onClick={() => handleFetch(row.machine)}
              >
                Fetch
              </CosButton>
              <CosButton
                type="ghost"
                size="sm"
                onClick={() => setDetails(row.machine)}
              >
                Details
              </CosButton>
              <CosButton
                type="ghost"
                size="sm"
                onClick={() => {
                  setEditing(row.machine)
                  setModalOpen(true)
                }}
              >
                Edit
              </CosButton>
              <CosButton
                type="warning"
                size="sm"
                onClick={() => handleDelete(row.machine)}
              >
                Delete
              </CosButton>
            </div>
          )}
        </Table.Column>
      </Table>

      <MachineModal
        isOpen={modalOpen}
        machine={editing}
        saving={saving}
        onCancel={() => {
          setModalOpen(false)
          setEditing(undefined)
        }}
        onSave={handleSave}
      />

      <MachineDetails machine={details} onClose={() => setDetails(null)} />

      <ImportMachinesModal
        isOpen={importOpen}
        onCancel={() => setImportOpen(false)}
        onDone={() => {
          setImportOpen(false)
          refresh()
        }}
      />
    </div>
  )
}
