// Assignment flow: pick a server (BMC machine) → pick the CubeCOS OS disk →
// map its real NICs to bonds/VLANs and tag roles. On finish it binds the
// machine to the node and rewrites the node's interface topology + role tags
// so the generated snapshot matches the assigned hardware.
import { useEffect, useMemo, useState } from 'react'
import { RoleIFKey, roleOptions } from '../../../model/roles'
import { formatBytes, Machine, NIC } from '../../../model/machine'
import { IF, IFInfo, NodeConfig, NodeRoleSettings } from '../../../model/types'
import { Select } from '../../../components/form/Select'
import { WizardModal, WizardStep } from '../../../components/WizardModal'
import { NicMapper, NicMapperValue } from './NicMapper'

export type AssignResult = {
  machineId: string
  osDisk: string
  node: NodeConfig
}

export type AssignServerFlowProps = {
  isOpen: boolean
  node: NodeConfig
  // Machines selectable: fetched, and either unassigned or already this node.
  machines: Machine[]
  currentMachineId?: string
  onCancel: () => void
  onFinish: (result: AssignResult) => void
}

const buildInitIFs = (ports: NIC[]): IF[] =>
  ports.map((_, i) => ({
    id: `if-${i}`,
    type: 'init' as const,
    name: `IF.${i + 1}`,
    enabled: true,
  }))

// assembleNode rewrites a node's interface topology + role tags from a
// completed mapper value, preserving IP config from the previous interfaces
// (matched by label). Pure, so it's unit-testable without the DnD UI.
export const assembleNode = (
  node: NodeConfig,
  mapper: NicMapperValue,
): NodeConfig => {
  const byLabel = new Map(
    [...node.initIFs, ...node.bondIFs, ...node.vlanIFs].map((f) => [f.name, f]),
  )
  const carryIP = (f: IF): IF => {
    const prev = byLabel.get(f.name)
    return prev
      ? { ...f, enabled: prev.enabled, IPAddr: prev.IPAddr, IPMask: prev.IPMask }
      : f
  }
  const all = [
    ...mapper.draft.initIFs,
    ...mapper.draft.bondIFs,
    ...mapper.draft.vlanIFs,
  ]
  const toInfo = (id: string | undefined): IFInfo => {
    const f = all.find((x) => x.id === id)
    return f ? { id: f.id, type: f.type } : {}
  }
  const roleSettings: NodeRoleSettings = {
    mgmtIF: {},
    storIF: {},
    storIFBackend: {},
  }
  for (const key of roleOptions[node.role]) {
    ;(roleSettings as Record<RoleIFKey, IFInfo>)[key] = toInfo(
      mapper.roleTags[key],
    )
  }
  const mgmtId = roleSettings.mgmtIF.id
  return {
    ...node,
    initIFs: mapper.draft.initIFs.map(carryIP),
    bondIFs: mapper.draft.bondIFs.map(carryIP),
    vlanIFs: mapper.draft.vlanIFs.map(carryIP),
    defaultIF: mgmtId ? toInfo(mgmtId) : node.defaultIF,
    roleSettings,
  }
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
  const [mapper, setMapper] = useState<NicMapperValue>({
    draft: { initIFs: [], bondIFs: [], vlanIFs: [] },
    roleTags: {},
  })

  const machine = machines.find((m) => m.id === machineId)

  // Seed defaults when the modal opens or the selected machine changes.
  useEffect(() => {
    if (!isOpen) return
    setMachineId(currentMachineId)
  }, [isOpen, currentMachineId])

  useEffect(() => {
    if (!machine?.inventory) {
      setPorts([])
      setMapper({ draft: { initIFs: [], bondIFs: [], vlanIFs: [] }, roleTags: {} })
      return
    }
    const nics = machine.inventory.nics ?? []
    setPorts(nics)
    setOsDisk(machine.assignment?.osDisk ?? machine.inventory.disks?.[0]?.name ?? '')
    setMapper({
      draft: { initIFs: buildInitIFs(nics), bondIFs: [], vlanIFs: [] },
      roleTags: {},
    })
  }, [machineId]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!isOpen) return null

  const reorderPort = (index: number, dir: -1 | 1) => {
    const j = index + dir
    if (j < 0 || j >= ports.length) return
    const next = [...ports]
    ;[next[index], next[j]] = [next[j], next[index]]
    setPorts(next)
    // Rebuild init IFs to match new order; drop bonds/vlans/tags to avoid
    // stale references.
    setMapper({
      draft: { initIFs: buildInitIFs(next), bondIFs: [], vlanIFs: [] },
      roleTags: {},
    })
  }

  const requiredRoles = roleOptions[node.role].filter(
    (k) => k !== 'storIFBackend',
  )
  const rolesComplete = requiredRoles.every((k) => !!mapper.roleTags[k])

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
                <label key={i} className="primary-body4 flex items-center gap-x-2">
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
      canNext: rolesComplete,
      content: machine?.inventory ? (
        <NicMapper
          role={node.role}
          ports={ports}
          value={mapper}
          onChange={setMapper}
          onReorderPort={reorderPort}
        />
      ) : (
        <p className="primary-body3">Select a fetched server first.</p>
      ),
    },
  ]

  const finish = () => {
    if (!machine) return
    onFinish({
      machineId: machine.id,
      osDisk,
      node: assembleNode(node, mapper),
    })
  }

  return (
    <WizardModal
      isOpen={isOpen}
      title={`Assign server to ${node.hostname}`}
      steps={steps}
      finishText="Assign"
      onCancel={onCancel}
      onFinish={finish}
    />
  )
}

// exported for tests
export { buildInitIFs }
