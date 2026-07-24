// Dry-run plan + confirm before powering real servers.
import { CosInlineNotification, CosModal } from '@cube-frontend/ui-library'
import { useEffect, useState } from 'react'
import { getDeployPlan, Plan, startDeploy } from '../../../api/deploy'

export type DeployModalProps = {
  isOpen: boolean
  clusterId: string
  onCancel: () => void
  onStarted: () => void
}

export const DeployModal = (props: DeployModalProps) => {
  const { isOpen, clusterId, onCancel, onStarted } = props
  const [plan, setPlan] = useState<Plan | null>(null)
  const [error, setError] = useState('')
  const [starting, setStarting] = useState(false)

  useEffect(() => {
    if (!isOpen) return
    setPlan(null)
    setError('')
    getDeployPlan(clusterId)
      .then(setPlan)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
  }, [isOpen, clusterId])

  if (!isOpen) return null

  const run = async () => {
    setStarting(true)
    setError('')
    try {
      await startDeploy(clusterId)
      onStarted()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setStarting(false)
    }
  }

  return (
    <CosModal
      isOpen={isOpen}
      title="Deploy cluster"
      size="md"
      actionText="Power on & deploy"
      actionButtonProps={{ disabled: !plan?.allAssigned || starting, loading: starting }}
      onActionClick={run}
      onCloseClick={onCancel}
    >
      <div className="flex flex-col gap-y-4">
        <CosInlineNotification type="warning" isClosable={false} title="This powers real servers">
          Each node below will be set to one-time PXE boot and power-cycled over
          IPMI, then re-imaged. Make sure these are the right machines.
        </CosInlineNotification>

        {error && (
          <CosInlineNotification type="error" title="Error" isClosable={false}>
            {error}
          </CosInlineNotification>
        )}

        {plan && !plan.allAssigned && (
          <CosInlineNotification type="error" isClosable={false} title="Not all nodes assigned">
            Every node needs a server assigned before deploy. Unassigned:{' '}
            {plan.nodes.filter((n) => !n.assigned).map((n) => n.hostname).join(', ')}
          </CosInlineNotification>
        )}

        {plan && (
          <div className="flex flex-col gap-y-2">
            {plan.nodes.map((n) => (
              <div
                key={n.hostname}
                className="secondary-body4 flex items-center gap-x-3 rounded-md border border-functional-border-divider px-3 py-2"
              >
                <span className="w-24 font-semibold">{n.hostname}</span>
                {n.assigned ? (
                  <span className="flex-1 text-functional-text-light">
                    {n.machineLabel} · BMC {n.bmcAddress} · disk {n.osDisk} ·{' '}
                    {(n.macs ?? []).join(', ')}
                  </span>
                ) : (
                  <span className="flex-1 text-status-negative">no server assigned</span>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </CosModal>
  )
}
