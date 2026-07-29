// Detailed installer-phase preflight report: node-by-node check matrix with
// problems pinned first, fed live from the polled deploy status. "Re-run
// preflight" rekicks a parked node in place — fresh check-in, new bundle +
// snapshot — with no PXE reboot.
import {
  CosButton,
  CosInlineNotification,
  CosModal,
  CosTag,
} from '@cube-frontend/ui-library'
import { useState } from 'react'
import {
  Deploy,
  DeployState,
  NodeDeploy,
  rekickPreflight,
} from '../../../api/deploy'

export type PreflightReportModalProps = {
  isOpen: boolean
  clusterId: string
  deploy: Deploy
  onClose: () => void
}

// A node can be rekicked only while its installer agent is still parked in the
// preflight phase; later states need a re-image.
const canRekick = (s: DeployState): boolean =>
  s === 'netbooting' || s === 'preflighting' || s === 'preflight-ok'

const NodeSection = (props: {
  clusterId: string
  node: NodeDeploy
  onRekicked: (msg: string) => void
}) => {
  const { clusterId, node: n, onRekicked } = props
  const [busy, setBusy] = useState(false)
  const pf = n.installerPreflight
  const rekick = async () => {
    setBusy(true)
    try {
      await rekickPreflight(clusterId, n.hostname)
      onRekicked(`${n.hostname}: preflight re-run requested — the agent re-checks in with the updated snapshot (no reboot)`)
    } catch (e) {
      onRekicked(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="flex flex-col gap-y-2 rounded-md border border-functional-border-divider p-3">
      <div className="flex items-center gap-x-3">
        <span className="primary-body3 font-semibold">{n.hostname}</span>
        {pf ? (
          <CosTag variant="filled" color={pf.passed ? 'cyan' : 'dark'}>
            {pf.passed ? 'passed' : 'not passed'}
          </CosTag>
        ) : (
          <CosTag variant="stroke" color="default">
            no report yet
          </CosTag>
        )}
        {pf && (
          <span className="secondary-body5 text-functional-text-light">
            clock skew {pf.clockSkewSec >= 0 ? '+' : ''}
            {pf.clockSkewSec.toFixed(2)}s · reported {pf.reportedAt}
          </span>
        )}
        <span className="flex-1" />
        <CosButton
          type="secondary"
          size="sm"
          disabled={!canRekick(n.state) || busy}
          loading={busy}
          onClick={rekick}
        >
          Re-run preflight
        </CosButton>
      </div>
      {pf && pf.matrix && pf.matrix.length > 0 && (
        <table className="w-full">
          <tbody>
            {pf.matrix.map((m, i) => (
              <tr
                key={i}
                className={m.ok ? '' : 'bg-status-negative/10'}
              >
                <td className="secondary-body5 w-6 py-0.5 pl-1">
                  <span className={m.ok ? 'text-status-positive' : 'font-bold text-status-negative'}>
                    {m.ok ? '✓' : '✗'}
                  </span>
                </td>
                <td className={`secondary-body5 py-0.5 ${m.ok ? '' : 'font-semibold text-status-negative'}`}>
                  {m.target}
                </td>
                <td className="secondary-body5 py-0.5 pr-1 text-functional-text-light">
                  {m.detail}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

type Health = 'ok' | 'error' | 'warn' | 'pending'

// nodeHealth derives a node's browsing color from its report: green passed,
// red any failing check, yellow reported-but-not-passed, grey no report yet.
const nodeHealth = (n: NodeDeploy): Health => {
  const pf = n.installerPreflight
  if (!pf) return 'pending'
  if (pf.matrix?.some((m) => !m.ok)) return 'error'
  if (pf.passed) return 'ok'
  return 'warn'
}

// Tab colors keyed by health — selected tab is filled, others are tinted.
const tabClass: Record<Health, { on: string; off: string }> = {
  ok: { on: 'bg-status-positive text-grey-0', off: 'bg-status-positive/15 text-status-positive' },
  error: { on: 'bg-status-negative text-grey-0', off: 'bg-status-negative/15 text-status-negative' },
  warn: { on: 'bg-status-warning text-grey-0', off: 'bg-status-warning/20 text-status-warning' },
  pending: { on: 'bg-grey-300 text-grey-0', off: 'bg-functional-hover-grey text-functional-text-light' },
}
const healthDot: Record<Health, string> = {
  ok: 'bg-status-positive',
  error: 'bg-status-negative',
  warn: 'bg-status-warning',
  pending: 'bg-grey-300',
}

export const PreflightReportModal = (props: PreflightReportModalProps) => {
  const { isOpen, clusterId, deploy, onClose } = props
  const [notice, setNotice] = useState('')
  const [selected, setSelected] = useState<string | null>(null)

  const nodes = Object.values(deploy.nodes).sort((a, b) =>
    a.hostname.localeCompare(b.hostname),
  )
  // Default to the first problematic node so the operator lands on what needs
  // attention; fall back to the first node.
  const attention = nodes.find((n) => nodeHealth(n) === 'error') ?? nodes[0]
  const activeHost = selected ?? attention?.hostname
  const active = nodes.find((n) => n.hostname === activeHost) ?? attention

  if (!isOpen) return null

  return (
    <CosModal
      isOpen={isOpen}
      title="Preflight report"
      size="md"
      actionText="Close"
      onActionClick={onClose}
      onCloseClick={onClose}
    >
      <div className="flex flex-col gap-y-3">
        {/* Node tabs — health-colored, click to switch (like the diagram's node boxes). */}
        <div className="flex flex-wrap gap-2">
          {nodes.map((n) => {
            const h = nodeHealth(n)
            const on = n.hostname === activeHost
            return (
              <button
                key={n.hostname}
                type="button"
                onClick={() => setSelected(n.hostname)}
                className={`primary-body4 flex items-center gap-x-2 rounded-md px-3 py-1.5 font-semibold transition ${
                  on ? tabClass[h].on : tabClass[h].off
                }`}
              >
                <span className={`inline-block h-2 w-2 rounded-full ${on ? 'bg-grey-0' : healthDot[h]}`} />
                {n.hostname}
              </button>
            )
          })}
        </div>

        {notice && (
          <CosInlineNotification type="neutral" isClosable={false} title="Re-run">
            {notice}
          </CosInlineNotification>
        )}

        {active && (
          <NodeSection clusterId={clusterId} node={active} onRekicked={setNotice} />
        )}
      </div>
    </CosModal>
  )
}
