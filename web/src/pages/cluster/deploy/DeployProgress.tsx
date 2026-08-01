// Live per-node deploy progress, polled while any node is still stepping.
import { CosButton, CosTag } from '@cube-frontend/ui-library'
import { useEffect, useRef, useState } from 'react'
import {
  advanceStep,
  cancelDeploy,
  Deploy,
  DeployState,
  getDeployStatus,
  isTerminal,
  Light,
  MANUAL_STEPS,
  Phase,
  PhaseCell,
  CellStatus,
} from '../../../api/deploy'
import { PreflightReportModal } from './PreflightReportModal'

// ManualStepBar shows the 5 gated steps with the current one highlighted and a
// Next button that clears the current gate. Disabled once past the last step.
const ManualStepBar = (props: {
  step: number
  canAdvance: boolean
  complete: boolean
  onNext: () => void
}) => {
  const { step, canAdvance, complete, onNext } = props // step is 1-based (1..6)
  const atLast = step >= MANUAL_STEPS.length
  return (
    <div className="flex items-center gap-x-3 rounded-md border border-primary/40 bg-primary/5 px-3 py-2">
      <span className="secondary-body5 font-semibold text-primary">Manual</span>
      <div className="flex flex-1 flex-wrap items-center gap-1.5">
        {MANUAL_STEPS.map((label, i) => {
          const n = i + 1
          // Once set_ready completes, the last step is done (green), not the
          // active/current highlight — otherwise it sits blue with nothing left
          // to advance to.
          const state = complete
            ? n <= step
              ? 'done'
              : 'todo'
            : n < step
              ? 'done'
              : n === step
                ? 'current'
                : 'todo'
          return (
            <span key={label} className="flex items-center gap-1.5">
              <span
                className={`secondary-body5 rounded px-2 py-0.5 font-medium ${
                  state === 'current'
                    ? 'bg-primary text-grey-0'
                    : state === 'done'
                      ? 'bg-status-positive/15 text-status-positive'
                      : 'bg-functional-hover-grey text-functional-text-light'
                }`}
              >
                {n}. {label}
              </span>
              {i < MANUAL_STEPS.length - 1 && (
                <span className="secondary-body6 text-functional-border-divider">›</span>
              )}
            </span>
          )
        })}
      </div>
      <CosButton
        type="primary"
        size="sm"
        disabled={atLast || !canAdvance}
        onClick={onNext}
      >
        {complete ? 'Done' : atLast ? 'Finishing' : 'Next'}
      </CosButton>
    </div>
  )
}

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

const cellClass: Record<CellStatus, string> = {
  pending: 'bg-functional-border-divider',
  active: 'bg-status-warning',
  done: 'bg-status-positive',
  error: 'bg-status-negative',
}
const cellLabel: Record<string, string> = {
  preflight: 'Preflight',
  restore: 'Restore',
  reboot: 'Reboot',
  apply: 'Apply',
  'set-ready': 'Set ready',
}

// ProgressStrip renders the per-node pipeline: preflight → restore → reboot →
// apply (+ a trailing set-ready cell on the master, which runs cluster
// set_ready). Each cell pending (grey) / active (pulsing yellow) / done (green)
// / error (red).
const ProgressStrip = ({ cells }: { cells: PhaseCell[] }) => (
  <div className="flex items-center gap-x-1.5">
    {cells.map((c, i) => (
      <span key={c.name} className="flex items-center gap-x-1">
        <span
          className="flex items-center gap-x-1"
          title={`${cellLabel[c.name] ?? c.name}: ${c.status}`}
        >
          <span
            className={`inline-block h-2.5 w-2.5 rounded-full ${cellClass[c.status]} ${
              c.status === 'active' ? 'animate-pulse' : ''
            }`}
          />
          <span className="secondary-body5 text-functional-text-light">
            {cellLabel[c.name] ?? c.name}
          </span>
        </span>
        {i < cells.length - 1 && (
          <span className="secondary-body6 text-functional-border-divider">›</span>
        )}
      </span>
    ))}
  </div>
)

export const DeployProgress = (props: DeployProgressProps) => {
  const { clusterId, reloadSignal } = props
  const [deploy, setDeploy] = useState<Deploy | null>(null)
  const [showReport, setShowReport] = useState(false)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const refresh = () => getDeployStatus(clusterId).then(setDeploy).catch(() => {})

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clusterId, reloadSignal])

  useEffect(() => {
    const nodes = deploy ? Object.values(deploy.nodes) : []
    // Keep polling while set_ready is still running too: every node is already
    // terminal (done) by then, so node state alone would stop the poll and the
    // final set-ready green would only appear on a manual refresh. Works for
    // both manual and auto (auto has no manualStep) — key it on all-nodes-done
    // plus set_ready not yet finished.
    const allDone = nodes.length > 0 && nodes.every((n) => n.state === 'done')
    const setReadyRunning = allDone && !deploy?.setReadyDone
    const active =
      (nodes.length > 0 && nodes.some((n) => !isTerminal(n.state))) ||
      setReadyRunning
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
        <CosButton type="secondary" size="sm" onClick={() => setShowReport(true)}>
          Preflight report
        </CosButton>
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
      <PreflightReportModal
        isOpen={showReport}
        clusterId={clusterId}
        deploy={deploy}
        onClose={() => setShowReport(false)}
      />
      {deploy.manual && (
        <ManualStepBar
          step={deploy.manualStep ?? 1}
          canAdvance={deploy.canAdvance ?? false}
          complete={!!deploy.setReadyDone}
          onNext={() => advanceStep(clusterId).then(refresh).catch(() => {})}
        />
      )}
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
              {n.progress && n.progress.length > 0 ? (
                <ProgressStrip cells={n.progress} />
              ) : (
                <>
                  <LightDot label="Net" value={n.light1} />
                  <LightDot label="Apply" value={n.light2} />
                </>
              )}
              {n.errCode && (
                <CosTag variant="filled" color="dark">
                  {n.errCode}
                </CosTag>
              )}
              {n.message && (
                <span
                  title={n.message}
                  className={`secondary-body5 max-w-[28rem] truncate ${n.state === 'error' ? 'text-status-negative' : 'text-functional-text-light'}`}
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
