// Dry-run plan + external-network (set_ready) input + confirm before powering
// real servers. The set_ready values are submitted up front so the run goes
// hands-free through to a ready cluster; they're pre-filled from the last saved
// value so a re-image reuses them.
import { CosInlineNotification, CosModal } from '@cube-frontend/ui-library'
import { useEffect, useState } from 'react'
import {
  getDeployPlan,
  getSetReady,
  Plan,
  startDeploy,
  submitSetReady,
} from '../../../api/deploy'

export type DeployModalProps = {
  isOpen: boolean
  clusterId: string
  onCancel: () => void
  onStarted: () => void
}

const Field = (props: {
  label: string
  value: string
  placeholder: string
  onChange: (v: string) => void
}) => (
  <label className="flex flex-col gap-y-1">
    <span className="secondary-body5 font-medium text-functional-text-secondary">
      {props.label}
    </span>
    <input
      className="primary-body4 rounded-md border border-functional-border-divider px-3 py-2 font-mono outline-none focus:border-primary"
      value={props.value}
      placeholder={props.placeholder}
      onChange={(e) => props.onChange(e.target.value)}
    />
  </label>
)

export const DeployModal = (props: DeployModalProps) => {
  const { isOpen, clusterId, onCancel, onStarted } = props
  const [plan, setPlan] = useState<Plan | null>(null)
  const [error, setError] = useState('')
  const [starting, setStarting] = useState(false)
  const [createExternal, setCreateExternal] = useState(true)
  const [cidr, setCidr] = useState('')
  const [gateway, setGateway] = useState('')
  const [ipRange, setIpRange] = useState('')

  useEffect(() => {
    if (!isOpen) return
    setPlan(null)
    setError('')
    getDeployPlan(clusterId)
      .then(setPlan)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
    // Pre-fill the external network from the last saved set_ready value so a
    // re-image reuses it instead of re-entering.
    getSetReady(clusterId)
      .then((s) => {
        if (!s) return
        setCreateExternal(s.createExternal)
        setCidr(s.cidr)
        setGateway(s.gateway)
        setIpRange(s.ipRange)
      })
      .catch(() => {})
  }, [isOpen, clusterId])

  if (!isOpen) return null

  const run = async () => {
    setStarting(true)
    setError('')
    try {
      // The external network is optional: no CIDR → set_ready finalizes WITHOUT
      // creating one. Submit the finalize input first (armed for the master to
      // consume once all nodes apply), then start the deploy.
      const willCreate = createExternal && cidr.trim() !== ''
      await submitSetReady(clusterId, {
        createExternal: willCreate,
        cidr,
        gateway,
        ipRange,
      })
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
      actionButtonProps={{
        disabled: !plan?.allAssigned || starting,
        loading: starting,
      }}
      onActionClick={run}
      onCloseClick={onCancel}
    >
      <div className="flex flex-col gap-y-4">
        <CosInlineNotification
          type="warning"
          isClosable={false}
          title="This powers real servers"
        >
          Each node below will be set to one-time PXE boot and power-cycled over
          IPMI, then re-imaged. Make sure these are the right machines.
        </CosInlineNotification>

        {error && (
          <CosInlineNotification type="error" title="Error" isClosable={false}>
            {error}
          </CosInlineNotification>
        )}

        {plan && !plan.allAssigned && (
          <CosInlineNotification
            type="error"
            isClosable={false}
            title="Not all nodes assigned"
          >
            Every node needs a server assigned before deploy. Unassigned:{' '}
            {plan.nodes
              .filter((n) => !n.assigned)
              .map((n) => n.hostname)
              .join(', ')}
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
                  <span className="flex-1 text-status-negative">
                    no server assigned
                  </span>
                )}
              </div>
            ))}
          </div>
        )}

        <div className="flex flex-col gap-y-3 rounded-md border border-functional-border-divider p-3">
          <span className="primary-body3 font-semibold">
            Finalize — cluster set ready
          </span>
          <p className="secondary-body5 text-functional-text-light">
            Runs <span className="font-mono">cluster set_ready</span> on the
            master once every node applies, bringing the cluster into service
            hands-free. The external network below is <b>optional</b> — leave it
            unchecked (or the CIDR blank) to finalize without a shared external
            network.
          </p>
          <label className="flex items-center gap-x-2">
            <input
              type="checkbox"
              checked={createExternal}
              onChange={(e) => setCreateExternal(e.target.checked)}
            />
            <span className="primary-body4 font-medium">
              Create a shared external network (optional)
            </span>
          </label>
          {createExternal && (
            <div className="flex flex-col gap-y-3 border-l-2 border-functional-border-divider pl-4">
              <Field
                label="External CIDR"
                value={cidr}
                placeholder="e.g. 10.32.0.0/16"
                onChange={setCidr}
              />
              <Field
                label="Gateway"
                value={gateway}
                placeholder="e.g. 10.32.0.254"
                onChange={setGateway}
              />
              <Field
                label="Floating IP pool"
                value={ipRange}
                placeholder="e.g. 10.32.5.10-10.32.5.99"
                onChange={setIpRange}
              />
            </div>
          )}
        </div>
      </div>
    </CosModal>
  )
}
