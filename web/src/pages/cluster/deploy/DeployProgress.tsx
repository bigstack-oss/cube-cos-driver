// Live per-node deploy progress, polled while any node is still stepping.
import { CosButton, CosTag } from '@cube-frontend/ui-library'
import { useEffect, useRef, useState } from 'react'
import {
  cancelDeploy,
  Deploy,
  DeployState,
  getDeployStatus,
  isTerminal,
  Light,
  Phase,
} from '../../../api/deploy'

export type DeployProgressProps = {
  clusterId: string
  // Bump to force an immediate refetch (e.g. right after starting a deploy).
  reloadSignal?: number
}

const stateColor: Record<
  DeployState,
  'default' | 'primary-blue' | 'cyan' | 'dark'
> = {
  pending: 'default',
  'bmc-preflight': 'primary-blue',
  'set-boot-pxe': 'primary-blue',
  'power-cycle': 'primary-blue',
  netbooting: 'primary-blue',
  preflighting: 'primary-blue',
  'preflight-ok': 'cyan',
  restoring: 'primary-blue',
  rebooting: 'primary-blue',
  imaging: 'primary-blue',
  imaged: 'cyan',
  'checked-in': 'cyan',
  'waiting-controller': 'default',
  'net-preflight': 'cyan',
  applying: 'cyan',
  applied: 'cyan',
  done: 'cyan',
  error: 'dark',
}

const phaseLabel: Record<Phase, string> = {
  boot: 'Boot from PXE',
  'preflight-net': 'Network preflight',
  time: 'Time sync',
  'wait-for-master': 'Wait for master',
  applying: 'Applying snapshot',
  done: 'Done',
  error: 'Error',
}

const lightClass: Record<Light, string> = {
  off: 'bg-functional-border-divider',
  yellow: 'bg-status-warning',
  green: 'bg-status-positive',
  red: 'bg-status-negative',
}

// LightDot renders one traffic-light gate with a label.
const LightDot = (props: { label: string; value: Light }) => (
  <span className="flex items-center gap-x-1" title={`${props.label}: ${props.value}`}>
    <span className={`inline-block h-2.5 w-2.5 rounded-full ${lightClass[props.value]}`} />
    <span className="secondary-body5 text-functional-text-light">{props.label}</span>
  </span>
)

export const DeployProgress = (props: DeployProgressProps) => {
  const { clusterId, reloadSignal } = props
  const [deploy, setDeploy] = useState<Deploy | null>(null)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const refresh = () => getDeployStatus(clusterId).then(setDeploy).catch(() => {})

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clusterId, reloadSignal])

  useEffect(() => {
    const nodes = deploy ? Object.values(deploy.nodes) : []
    const active = nodes.length > 0 && nodes.some((n) => !isTerminal(n.state))
    if (active && !pollRef.current) {
      pollRef.current = setInterval(refresh, 1500)
    } else if (!active && pollRef.current) {
      clearInterval(pollRef.current)
      pollRef.current = null
    }
    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current)
        pollRef.current = null
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [deploy])

  if (!deploy) return null
  const nodes = Object.values(deploy.nodes).sort((a, b) =>
    a.hostname.localeCompare(b.hostname),
  )
  const anyActive = nodes.some((n) => !isTerminal(n.state))

  return (
    <div className="flex flex-col gap-y-3 rounded-md border border-functional-border-divider p-4">
      <div className="flex items-center gap-x-3">
        <span className="primary-h4">Deployment</span>
        {anyActive && (
          <CosButton
            type="warning"
            size="sm"
            onClick={() => cancelDeploy(clusterId).then(refresh)}
          >
            Cancel
          </CosButton>
        )}
      </div>
      <div className="flex flex-col gap-y-2">
        {nodes.map((n) => (
          <div key={n.hostname} className="flex flex-col gap-y-1">
            <div className="flex items-center gap-x-3">
              <span className="primary-body3 w-28 font-semibold">
                {n.hostname}
              </span>
              <CosTag
                variant={n.state === 'error' ? 'filled' : 'stroke'}
                color={stateColor[n.state]}
              >
                {phaseLabel[n.phase]}
              </CosTag>
              <LightDot label="Net" value={n.light1} />
              <LightDot label="Apply" value={n.light2} />
              {n.errCode && (
                <CosTag variant="filled" color="dark">
                  {n.errCode}
                </CosTag>
              )}
              {n.message && (
                <span
                  className={`secondary-body5 ${n.state === 'error' ? 'text-status-negative' : 'text-functional-text-light'}`}
                >
                  {n.message}
                </span>
              )}
            </div>
            {n.preflight && n.preflight.length > 0 && (
              <div className="ml-28 flex flex-wrap gap-1">
                {n.preflight.map((p, i) => (
                  <CosTag
                    key={i}
                    variant="stroke"
                    color={p.ok ? 'cyan' : 'dark'}
                  >
                    {`${p.target}${p.detail ? ` (${p.detail})` : ''}`}
                  </CosTag>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
