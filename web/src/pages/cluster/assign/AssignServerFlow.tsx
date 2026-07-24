// Assignment flow: pick a server (BMC machine) → pick the CubeCOS OS disk →
// bind its real NICs to the snapshot's network topology (correcting IF.N
// picks with real hardware info). Binds the machine to the node and applies
// any interface-label corrections back onto the node config.
import { useEffect, useMemo, useState } from 'react'
import { formatBytes, Machine, NIC } from '../../../model/machine'
import { NodeConfig } from '../../../model/types'
import { Select } from '../../../components/form/Select'
import { WizardModal, WizardStep } from '../../../components/WizardModal'
import { SnapshotNetworkMapper } from './SnapshotNetworkMapper'

export type AssignResult = {
  machineId: string
  osDisk: string
  node: NodeConfig
}

export type AssignServerFlowProps = {
  isOpen: boolean
  node: NodeConfig
  machines: Machine[]
  currentMachineId?: string
  onCancel: () => void
  onFinish: (result: AssignResult) => void
}

export const AssignServerFlow = (props: AssignServerFlowProps) => {
  const { isOpen, node, machines, currentMachineId, onCancel, onFinish } = props

  const selectable = useMemo(
    () =>
      machines.filter(
        (m) =>
          m.fetchState === 'ok' &&
          m.inventory &&
          (!m.assignment || m.id === currentMachineId),
      ),
    [machines, currentMachineId],
  )

  const [machineId, setMachineId] = useState<string | undefined>()
  const [osDisk, setOsDisk] = useState<string>('')
  const [ports, setPorts] = useState<NIC[]>([])
  const [workingNode, setWorkingNode] = useState<NodeConfig>(node)

  const machine = machines.find((m) => m.id === machineId)

  useEffect(() => {
    if (!isOpen) return
    setMachineId(currentMachineId)
    setWorkingNode(node)
  }, [isOpen, currentMachineId, node])

  useEffect(() => {
    if (!machine?.inventory) {
      setPorts([])
      return
    }
    setPorts(machine.inventory.nics ?? [])
    setOsDisk(
      machine.assignment?.osDisk ?? machine.inventory.disks?.[0]?.name ?? '',
    )
  }, [machineId]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!isOpen) return null

  const steps: WizardStep[] = [
    {
      label: 'Server',
      canNext: !!machine?.inventory,
      content: (
        <div className="flex flex-col gap-y-4">
          {selectable.length === 0 ? (
            <p className="primary-body3">
              No fetched machines available. Add a machine on the Hardware page
              and run <strong>Fetch</strong> first.
            </p>
          ) : (
            <Select<string>
              label="Server (BMC machine)"
              value={machineId}
              options={selectable.map((m) => ({
                value: m.id,
                label: `${m.label} — ${m.inventory?.serial ?? m.bmc.address}`,
              }))}
              onChange={setMachineId}
            />
          )}
          {machine?.inventory && (
            <p className="secondary-body4 text-functional-text-light">
              {`${machine.inventory.cpuModel ?? 'CPU'} ×${machine.inventory.cpuCount ?? 1}, ` +
                `${formatBytes(machine.inventory.memoryBytes)}, ` +
                `${machine.inventory.nics?.length ?? 0} NIC(s), ` +
                `${machine.inventory.disks?.length ?? 0} disk(s)`}
            </p>
          )}
        </div>
      ),
    },
    {
      label: 'OS disk',
      canNext: osDisk !== '',
      content: (
        <div className="flex flex-col gap-y-2">
          <span className="primary-body3 font-semibold">
            CubeCOS install disk
          </span>
          {(machine?.inventory?.disks ?? []).length === 0 ? (
            <p className="secondary-body4 text-functional-text-light">
              No disks discovered on this machine.
            </p>
          ) : (
            (machine?.inventory?.disks ?? []).map((d, i) => {
              const name = d.name || d.model || `disk-${i}`
              return (
                <label
                  key={i}
                  className="primary-body4 flex items-center gap-x-2"
                >
                  <input
                    type="radio"
                    name="os-disk"
                    checked={osDisk === name}
                    onChange={() => setOsDisk(name)}
                  />
                  {`${name}${d.type ? ` · ${d.type}` : ''}${d.sizeBytes ? ` · ${formatBytes(d.sizeBytes)}` : ''}`}
                </label>
              )
            })
          )}
        </div>
      ),
    },
    {
      label: 'Network',
      canNext: true,
      content: machine?.inventory ? (
        <SnapshotNetworkMapper
          node={workingNode}
          ports={ports}
          onChange={setWorkingNode}
        />
      ) : (
        <p className="primary-body3">Select a fetched server first.</p>
      ),
    },
  ]

  return (
    <WizardModal
      isOpen={isOpen}
      title={`Assign server to ${node.hostname}`}
      steps={steps}
      finishText="Assign"
      onCancel={onCancel}
      onFinish={() => {
        if (!machine) return
        onFinish({ machineId: machine.id, osDisk, node: workingNode })
      }}
    />
  )
}
