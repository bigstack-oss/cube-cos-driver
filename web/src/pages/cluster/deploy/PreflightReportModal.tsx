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

export const PreflightReportModal = (props: PreflightReportModalProps) => {
  const { isOpen, clusterId, deploy, onClose } = props
  const [notice, setNotice] = useState('')
  if (!isOpen) return null

  const nodes = Object.values(deploy.nodes).sort((a, b) =>
    a.hostname.localeCompare(b.hostname),
  )
  const problems = nodes.flatMap((n) =>
    (n.installerPreflight?.matrix ?? [])
      .filter((m) => !m.ok)
      .map((m) => ({ node: n.hostname, ...m })),
  )

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
        {notice && (
          <CosInlineNotification type="neutral" isClosable={false} title="Re-run">
            {notice}
          </CosInlineNotification>
        )}
        {problems.length > 0 ? (
          <CosInlineNotification
            type="error"
            isClosable={false}
            title={`${problems.length} failing check(s)`}
          >
            {problems.map((p, i) => (
              <div key={i}>
                <b>{p.node}</b> — {p.target}
                {p.detail ? `: ${p.detail}` : ''}
              </div>
            ))}
          </CosInlineNotification>
        ) : (
          <CosInlineNotification
            type="positive"
            isClosable={false}
            title="All reported checks passing"
          >
            Every node that has reported shows a fully green matrix.
          </CosInlineNotification>
        )}
        {nodes.map((n) => (
          <NodeSection
            key={n.hostname}
            clusterId={clusterId}
            node={n}
            onRekicked={setNotice}
          />
        ))}
      </div>
    </CosModal>
  )
}
