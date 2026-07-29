import { CosButton, CosModal, CosTag, GetCosBasicTable } from '@cube-frontend/ui-library'
import { useCallback, useEffect, useRef, useState } from 'react'
import {
  createMachine,
  deleteMachine,
  getInspectStatus,
  InspectStatus,
  listMachines,
  startInspect,
  updateMachine,
} from '../../api/machines'
import { ImagePicker } from '../../components/ImagePicker'
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
  machine: Machine
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
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [inspectConfirm, setInspectConfirm] = useState(false)
  const [inspectImage, setInspectImage] = useState('')
  const [inspects, setInspects] = useState<InspectStatus[]>([])
  const [modalError, setModalError] = useState('')
  const [deletingMachine, setDeletingMachine] = useState<Machine | null>(null)
  const inspectPoll = useRef<ReturnType<typeof setInterval> | null>(null)

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

  const handleSave = async (input: MachineInput) => {
    setSaving(true)
    setModalError('')
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
      // Keep the modal open with the verify error so the operator can correct
      // the address/credentials — nothing was stored.
      setModalError(e instanceof Error ? e.message : String(e))
    } finally {
      setSaving(false)
    }
  }

  const confirmDelete = async () => {
    if (!deletingMachine) return
    try {
      // Delete cascades: the assignment lives on the machine record, so removing
      // it unassigns any cluster node that referenced it (snapshots are kept).
      await deleteMachine(deletingMachine.id)
      setDeletingMachine(null)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const refreshInspects = useCallback(() => {
    getInspectStatus()
      .then(setInspects)
      .catch(() => {})
  }, [])
  useEffect(refreshInspects, [refreshInspects])
  useEffect(() => {
    const active = inspects.some((s) => s.state === 'booting')
    if (active && !inspectPoll.current) {
      inspectPoll.current = setInterval(() => {
        refreshInspects()
        refresh()
      }, 3000)
    } else if (!active && inspectPoll.current) {
      clearInterval(inspectPoll.current)
      inspectPoll.current = null
    }
    return () => {
      if (inspectPoll.current) {
        clearInterval(inspectPoll.current)
        inspectPoll.current = null
      }
    }
  }, [inspects, refreshInspects, refresh])

  const toggle = (id: string) =>
    setSelected((s) => {
      const n = new Set(s)
      if (n.has(id)) n.delete(id)
      else n.add(id)
      return n
    })

  const handleInspect = async () => {
    try {
      await startInspect([...selected], inspectImage)
      setInspectConfirm(false)
      setSelected(new Set())
      refreshInspects()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const inspectColor = (s: string): 'primary-blue' | 'cyan' | 'dark' =>
    s === 'reported' ? 'cyan' : s === 'error' ? 'dark' : 'primary-blue'

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
            setModalError('')
            setModalOpen(true)
          }}
        >
          Add machine
        </CosButton>
        <CosButton type="secondary" onClick={() => setImportOpen(true)}>
          Import from file
        </CosButton>
        <CosButton
          type="secondary"
          disabled={selected.size === 0}
          onClick={() => setInspectConfirm(true)}
        >
          {`Inspect selected (${selected.size})`}
        </CosButton>
        {error && (
          <span className="primary-body4 text-status-negative">{error}</span>
        )}
      </div>

      {inspects.length > 0 && (
        <div className="flex flex-col gap-y-2 rounded-md border border-functional-border-divider p-4">
          <span className="primary-h4">Hardware inspect</span>
          <div className="flex flex-wrap gap-2">
            {inspects.map((s) => (
              <CosTag key={s.machineId} variant="stroke" color={inspectColor(s.state)}>
                {`${s.label}: ${s.state === 'booting' ? 'inspecting…' : s.state}`}
              </CosTag>
            ))}
          </div>
        </div>
      )}

      <Table rows={rows} isLoading={loading}>
        <Table.Column label="" property="id" fitContent>
          {(id: string) => (
            <input
              type="checkbox"
              checked={selected.has(id)}
              onChange={() => toggle(id)}
              aria-label="select for inspect"
            />
          )}
        </Table.Column>
        <Table.Column label="Label" property="label" emphasize />
        <Table.Column label="BMC address" property="address" />
        <Table.Column label="Serial" property="serial" />
        <Table.Column label="CPU" property="cpu" />
        <Table.Column label="Memory" property="memory" />
        <Table.Column label="NICs" property="nics" />
        <Table.Column label="Disks" property="disks" />
        <Table.Column label="Actions" property="id" fitContent>
          {(_: string, row: MachineRow) => (
            <div className="flex gap-x-1">
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
                  setModalError('')
                  setModalOpen(true)
                }}
              >
                Edit
              </CosButton>
              <CosButton
                type="warning"
                size="sm"
                onClick={() => setDeletingMachine(row.machine)}
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
        error={modalError}
        onCancel={() => {
          setModalOpen(false)
          setEditing(undefined)
          setModalError('')
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

      {inspectConfirm && (
        <CosModal
          isOpen
          size="sm"
          title="Inspect servers"
          actionText="Power-cycle & inspect"
          onActionClick={handleInspect}
          onCloseClick={() => setInspectConfirm(false)}
        >
          <p className="primary-body3">
            This will <b>power-cycle {selected.size} server(s)</b> and boot them
            into a hardware-discovery pass — each reports its CPU, memory, disks,
            and NICs, then powers off. Only run on servers that are safe to
            reboot.
          </p>
          <div className="mt-3">
            <ImagePicker onChange={setInspectImage} />
          </div>
        </CosModal>
      )}

      {deletingMachine && (
        <CosModal
          isOpen
          size="sm"
          title={`Delete ${deletingMachine.label}?`}
          actionText="Delete"
          onActionClick={confirmDelete}
          onCloseClick={() => setDeletingMachine(null)}
        >
          <div className="flex flex-col gap-y-2">
            <p className="primary-body3">
              Removes this server entry and its hardware inventory.
            </p>
            {(deletingMachine.assignments?.length ?? 0) > 0 && (
              <>
                <p className="primary-body4 text-status-negative">
                  It is assigned to {deletingMachine.assignments!.length} cluster
                  node(s) — deleting will unassign:
                </p>
                <ul className="secondary-body4 list-inside list-disc text-functional-text-light">
                  {deletingMachine.assignments!.map((a, i) => (
                    <li key={i}>
                      {a.hostname} in cluster {a.clusterId}
                    </li>
                  ))}
                </ul>
                <p className="secondary-body5 text-functional-text-light">
                  Their snapshots are kept; re-assign a server before deploying.
                </p>
              </>
            )}
          </div>
        </CosModal>
      )}
    </div>
  )
}
