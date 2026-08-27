// Assignment flow: pick a server (BMC machine) → pick the CubeCOS OS disk →
// bind its real NICs to the snapshot's network topology (correcting IF.N
// picks with real hardware info). Binds the machine to the node and applies
// any interface-label corrections back onto the node config.
import { CosButton } from '@cube-frontend/ui-library'
import { useEffect, useMemo, useState } from 'react'
import {
  cosNics,
  formatBytes,
  Machine,
  NIC,
  osEligibleDisks,
} from '../../../model/machine'
import { NodeConfig } from '../../../model/types'
import { Select } from '../../../components/form/Select'
import { WizardModal, WizardStep } from '../../../components/WizardModal'
import { NodeWizard } from '../../../components/wizards/node/NodeWizard'
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

  // Edit-node-network sub-modal: opened from the mapper when the real NICs
  // don't match the node's snapshot network. Saving returns to the mapper with
  // the updated topology.
  const [editNet, setEditNet] = useState(false)

  // A server may be assigned to multiple nodes/clusters, so any inventoried
  // machine is selectable (a same-cluster duplicate is flagged, not blocked).
  const selectable = useMemo(
    () => machines.filter((m) => m.fetchState === 'ok' && m.inventory),
    [machines],
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
    setPorts(cosNics(machine.inventory.nics))
    // Default to the assigned disk, else the first OS-eligible (local physical)
    // disk — never a SAN LUN or virtual media.
    setOsDisk(
      machine.assignment?.osDisk ??
        osEligibleDisks(machine.inventory)[0]?.name ??
        '',
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
                `${osEligibleDisks(machine.inventory).length} local disk(s)`}
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
          {(() => {
            const eligible = osEligibleDisks(machine?.inventory)
            const total = machine?.inventory?.disks?.length ?? 0
            const excluded = total - eligible.length
            if (eligible.length === 0) {
              return (
                <p className="secondary-body4 text-functional-text-light">
                  No local install disks discovered
                  {total > 0
                    ? ` (${total} SAN/virtual/RAID-member device(s) excluded — not valid OS targets)`
                    : ''}
                  .
                </p>
              )
            }
            return (
              <>
                {eligible.map((d, i) => {
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
                })}
                {excluded > 0 && (
                  <p className="secondary-body5 text-functional-text-light">
                    {excluded} SAN / virtual / RAID-member device(s) hidden — only OS-visible local
                    physical disks can host the OS.
                  </p>
                )}
              </>
            )
          })()}
        </div>
      ),
    },
    {
      label: 'Network',
      canNext: true,
      content: machine?.inventory ? (
        <div className="flex flex-col gap-y-3">
          <div className="flex items-center justify-end">
            <CosButton
              type="secondary"
              size="sm"
              onClick={() => setEditNet(true)}
            >
              Edit node network
            </CosButton>
          </div>
          <SnapshotNetworkMapper
            node={workingNode}
            ports={ports}
            onChange={setWorkingNode}
          />
        </div>
      ) : (
        <p className="primary-body3">Select a fetched server first.</p>
      ),
    },
  ]

  return (
    <>
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
      {/* On-the-fly node-network edit: Network + Role (role type locked), no
          Hostname. Saving updates the working node so the mapper reflects the
          new topology. */}
      <NodeWizard
        isOpen={editNet}
        initial={workingNode}
        takenHostnames={[]}
        hideHostname
        lockRole
        title={`Edit node network — ${node.hostname}`}
        onCancel={() => setEditNet(false)}
        onFinish={(updated) => {
          setWorkingNode(updated)
          setEditNet(false)
        }}
      />
    </>
  )
}
