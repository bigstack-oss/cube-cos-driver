// Node settings wizard: Hostname → Network → Role.
import { CosInput } from '@cube-frontend/ui-library'
import { useEffect, useState } from 'react'
import { roleOptions } from '../../../model/roles'
import { IF, NodeConfig, NodeRole, NodeRoleSettings } from '../../../model/types'
import { isValidHostname, isValidIPv4 } from '../../../model/validate'
import { newId } from '../../../utils/random'
import { WizardModal, WizardStep } from '../../WizardModal'
import { NetworkStep } from './NetworkStep'
import {
  changeIF,
  defaultNodeDraft,
  IFDraft,
  pruneDefaultIF,
  pruneRoleSettings,
} from './nodeDraft'
import { RoleStep } from './RoleStep'

export type NodeWizardProps = {
  isOpen: boolean
  // Edit mode when set.
  initial?: NodeConfig
  // Hostnames of the other nodes in the cluster (duplicate guard).
  takenHostnames: string[]
  // Network-edit mode (from the assign flow): drop the Hostname step and lock
  // the role type, so only network + role interface mapping can change.
  hideHostname?: boolean
  lockRole?: boolean
  title?: string
  onCancel: () => void
  onFinish: (node: NodeConfig) => void
}

export const NodeWizard = (props: NodeWizardProps) => {
  const {
    isOpen,
    initial,
    takenHostnames,
    hideHostname,
    lockRole,
    title,
    onCancel,
    onFinish,
  } = props

  const [node, setNode] = useState<NodeConfig>(() => defaultNodeDraft(newId))

  useEffect(() => {
    if (!isOpen) return
    setNode(initial ?? { ...defaultNodeDraft(newId), id: newId() })
  }, [isOpen, initial])

  const draft: IFDraft = {
    initIFs: node.initIFs,
    bondIFs: node.bondIFs,
    vlanIFs: node.vlanIFs,
  }

  // Apply an IF-draft change and prune selections that became invalid
  // (legacy pruning effects, done synchronously here).
  const applyDraft = (next: IFDraft) => {
    setNode((prev) => ({
      ...prev,
      initIFs: next.initIFs,
      bondIFs: next.bondIFs,
      vlanIFs: next.vlanIFs,
      defaultIF: pruneDefaultIF(next, prev.defaultIF),
      roleSettings: pruneRoleSettings(next, prev.role, prev.roleSettings),
    }))
  }

  const handleIFChange = (target: IF, patch: Partial<IF>) => {
    applyDraft(changeIF(draft, target, patch))
  }

  const hostnameError = (() => {
    if (node.hostname === '') return 'The field should not be blank'
    if (!isValidHostname(node.hostname)) return 'Invalid hostname'
    if (takenHostnames.includes(node.hostname)) {
      return `Hostname ${node.hostname} is already used in this cluster`
    }
    return undefined
  })()

  const networkValid =
    !!node.defaultIF.id &&
    isValidIPv4(node.defaultGateway) &&
    [...node.initIFs, ...node.bondIFs, ...node.vlanIFs].every(
      (f) =>
        !f.enabled ||
        ((f.IPAddr === undefined || f.IPAddr === '' || isValidIPv4(f.IPAddr)) &&
          (f.IPMask === undefined || f.IPMask === '' || isValidIPv4(f.IPMask))),
    )

  const roleValid = roleOptions[node.role].every((key) => {
    if (key === 'storIFBackend') return true
    return !!node.roleSettings[key]?.id
  })

  const hostnameStep: WizardStep = {
    label: 'Hostname',
    canNext: hostnameError === undefined,
    content: (
      <CosInput
        label="Hostname"
        value={node.hostname}
        onChange={(e) =>
          setNode((prev) => ({ ...prev, hostname: e.target.value }))
        }
        errorMessage={hostnameError}
      />
    ),
  }
  const steps: WizardStep[] = [
    ...(hideHostname ? [] : [hostnameStep]),
    {
      label: 'Network',
      canNext: networkValid,
      content: (
        <NetworkStep
          draft={draft}
          defaultIFId={node.defaultIF.id}
          defaultGateway={node.defaultGateway}
          onDraftChange={applyDraft}
          onIFChange={handleIFChange}
          onDefaultIFChange={(IF) =>
            setNode((prev) => ({
              ...prev,
              defaultIF: { id: IF.id, type: IF.type },
            }))
          }
          onDefaultGatewayChange={(defaultGateway) =>
            setNode((prev) => ({ ...prev, defaultGateway }))
          }
        />
      ),
    },
    {
      label: 'Role',
      canNext: roleValid,
      content: (
        <RoleStep
          draft={draft}
          role={node.role}
          roleSettings={node.roleSettings}
          lockRole={lockRole}
          onRoleChange={(role: NodeRole, settings: NodeRoleSettings) =>
            setNode((prev) => ({ ...prev, role, roleSettings: settings }))
          }
          onRoleSettingsChange={(roleSettings) =>
            setNode((prev) => ({ ...prev, roleSettings }))
          }
        />
      ),
    },
  ]

  return (
    <WizardModal
      isOpen={isOpen}
      title={title ?? 'Node Snapshot Configuration'}
      steps={steps}
      finishText="Save"
      onCancel={onCancel}
      onFinish={() => {
        // storIFBackend must always exist in the payload ({} when unset).
        const roleSettings = { ...node.roleSettings }
        if (!roleSettings.storIFBackend) roleSettings.storIFBackend = {}
        onFinish({ ...node, roleSettings })
      }}
    />
  )
}
