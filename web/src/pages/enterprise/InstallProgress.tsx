// Live enterprise-module install progress: polls getInstall while running,
// renders the step list with streamed output, manual Next gating, cancel,
// and the completion card. Mirrors DeployProgress's polling shape.
import { CosButton, CosTag } from '@cube-frontend/ui-library'
import ChevronRight from '@cube-frontend/ui-library/icons/monochrome/chevron_right.svg?react'
import { useEffect, useRef, useState } from 'react'
import {
  cancelInstall,
  getInstall,
  Install,
  Module,
  nextStep,
  StepState,
} from '../../api/enterprise'

export type InstallProgressProps = {
  clusterId: string
  module: Module
  install: Install
  onClose: () => void
}

const MODULE_LABEL: Record<Module, string> = {
  appfw: 'App-Framework',
  cmp: 'CubeCMP',
}

const stepStateColor: Record<StepState, 'default' | 'primary-blue' | 'cyan' | 'dark'> = {
  pending: 'default',
  active: 'primary-blue',
  done: 'cyan',
  error: 'dark',
  skipped: 'default',
}

const RUNBOOK_URL = 'https://docs.bigstack.co/docs/cubecos/enterprise-modules'

// Step number-badge colour: green once passed, dark while active, red on
// error, grey while pending.
const stepBadgeColor = (s: StepState): string => {
  switch (s) {
    case 'done':
    case 'skipped':
      return 'bg-status-positive'
    case 'active':
      return 'bg-functional-text'
    case 'error':
      return 'bg-status-negative'
    default:
      return 'bg-functional-disable-text'
  }
}

export function InstallProgress({ clusterId, module, install, onClose }: InstallProgressProps) {
  const [inst, setInst] = useState<Install>(install)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const preflightFired = useRef(false)

  const refresh = () => getInstall(clusterId, module).then(setInst).catch(() => {})

  // Preflight is read-only, so run it automatically right after Install for a
  // manual install (the operator gates only the mutating steps via Next).
  useEffect(() => {
    if (
      !preflightFired.current &&
      inst.Manual &&
      inst.State === 'running' &&
      inst.Current === 0 &&
      inst.Steps[0]?.Name === 'preflight'
    ) {
      preflightFired.current = true
      nextStep(clusterId, module)
        .then(setInst)
        .catch(() => {})
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    if (inst.State === 'running' && !pollRef.current) {
      pollRef.current = setInterval(refresh, 1500)
    } else if (inst.State !== 'running' && pollRef.current) {
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
  }, [inst.State])

  const currentStep = inst.Steps[inst.Current] ?? inst.Steps[inst.Steps.length - 1]
  const errorStep = inst.Steps.find((s) => s.State === 'error')
  // Show the furthest-progressed step's result (the one that just ran or is
  // running), so the operator reviews it before clicking Next.
  const lastResulted = [...inst.Steps]
    .reverse()
    .find((s) => s.State !== 'pending')
  const detailStep = errorStep ?? lastResulted ?? currentStep
  const running = inst.State === 'running'
  const showNext = running && inst.Manual === true
  const nextDisabled = currentStep?.State === 'active'

  return (
    <div role="status" aria-label="install progress" className="flex flex-col gap-y-4">
      <div className="flex items-center gap-x-3">
        <span className="primary-h4">{MODULE_LABEL[module]} install</span>
        <span className="secondary-body5 text-functional-text-light">
          {inst.Manual ? 'Manual' : 'Automatic'}
        </span>
        <div className="flex-1" />
        {showNext && (
          <CosButton
            type="primary"
            size="sm"
            disabled={nextDisabled}
            onClick={() => nextStep(clusterId, module).then(setInst).catch(() => {})}
          >
            Next
          </CosButton>
        )}
        {running && (
          <CosButton
            type="warning"
            size="sm"
            onClick={() => cancelInstall(clusterId, module).then(refresh).catch(() => {})}
          >
            Cancel
          </CosButton>
        )}
      </div>

      {/* Horizontal step progression (like the snapshot wizard); the number
          badge turns green once a step passes. */}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        {inst.Steps.map((s, i) => (
          <div key={s.Name} className="flex shrink-0 items-center gap-3">
            <div className="flex items-center gap-3">
              <div
                className={`secondary-body7 flex size-4 shrink-0 items-center justify-center rounded-full font-extrabold text-white ${stepBadgeColor(
                  s.State,
                )}`}
              >
                {i + 1}
              </div>
              <p
                className={`primary-body3 ${
                  s.State === 'pending'
                    ? 'text-functional-disable-text'
                    : 'font-semibold text-functional-text'
                }`}
              >
                {s.Title || s.Name}
              </p>
            </div>
            {i < inst.Steps.length - 1 && (
              <ChevronRight className="icon-lg text-functional-text-light" />
            )}
          </div>
        ))}
      </div>

      {/* Current (or errored) step status + streamed output — no scrolling
          through the whole list. */}
      {detailStep && (
        <div className="flex flex-col gap-y-2 rounded-md border border-functional-border-divider p-3">
          <div className="flex items-center gap-x-3">
            <span className="primary-body3 font-semibold">
              {detailStep.Title || detailStep.Name}
            </span>
            <CosTag
              variant={detailStep.State === 'error' ? 'filled' : 'stroke'}
              color={stepStateColor[detailStep.State]}
            >
              {detailStep.State}
            </CosTag>
          </div>
          {detailStep.Output && (
            <pre className="secondary-body6 max-h-64 overflow-y-auto rounded-md bg-functional-hover-grey p-2 font-mono text-functional-text-light">
              {detailStep.Output}
            </pre>
          )}
        </div>
      )}

      {inst.State === 'done' && (
        <div className="flex flex-col gap-y-2 rounded-md border border-status-positive/40 bg-status-positive/5 p-3">
          <span className="primary-body3 font-semibold">
            {`✅ ${MODULE_LABEL[module]} installed`}
          </span>
          {module === 'cmp' && (
            <>
              <p className="secondary-body5">
                Portal:{' '}
                <a
                  href={inst.Portal}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-primary underline"
                >
                  {inst.Portal}
                </a>
              </p>
              <p className="secondary-body5 text-functional-text-light">
                Log in at the portal as <code>admin</code> (Keycloak: <code>admin</code> /{' '}
                <code>admin</code>). The administrator permission is granted automatically. See
                the{' '}
                {/* TODO: final runbook URL */}
                <a href={RUNBOOK_URL} target="_blank" rel="noopener noreferrer" className="text-primary underline">
                  runbook
                </a>{' '}
                for details.
              </p>
            </>
          )}
        </div>
      )}

      {inst.State === 'error' && (
        <div className="flex flex-col gap-y-1 rounded-md border border-status-negative/40 bg-status-negative/5 p-3">
          <span className="primary-body3 font-semibold text-status-negative">
            Install failed
          </span>
          {errorStep && (
            <p className="secondary-body5 text-status-negative">{errorStep.Err}</p>
          )}
        </div>
      )}

      {inst.State === 'cancelled' && (
        <div className="rounded-md border border-functional-border-divider p-3">
          <span className="secondary-body5 text-functional-text-light">
            Installation cancelled
          </span>
        </div>
      )}

      <div>
        <CosButton type="secondary" size="sm" onClick={onClose}>
          Close
        </CosButton>
      </div>
    </div>
  )
}
