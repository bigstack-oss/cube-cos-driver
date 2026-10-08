// Release x module support matrix (GET /api/v1/enterprise/matrix).
import { CosTag } from '@cube-frontend/ui-library'
import { Manifest, MatrixStatus } from '../../api/enterprise'

const STATUS_TAG: Record<
  MatrixStatus,
  { color: 'default' | 'primary-blue' | 'cyan' | 'dark'; variant: 'filled' | 'stroke' }
> = {
  supported: { color: 'cyan', variant: 'stroke' },
  untested: { color: 'default', variant: 'stroke' },
  deprecated: { color: 'primary-blue', variant: 'stroke' },
  blocked: { color: 'dark', variant: 'filled' },
}

const MODULE_COLS = [
  { key: 'cmp', label: 'CubeCMP' },
  { key: 'advisor', label: 'Cube AI Advisor' },
]

export const SupportMatrix = ({ manifests }: { manifests: Manifest[] }) => {
  if (manifests.length === 0) return null
  return (
    <div className="flex flex-col gap-y-3">
      <h2 className="primary-body1 font-semibold">Support matrix</h2>
      <div className="overflow-x-auto rounded-lg border border-functional-border-divider">
        <table className="w-full text-left">
          <thead className="secondary-body5 text-functional-text-secondary">
            <tr>
              <th className="px-4 py-2 font-medium">Release</th>
              {MODULE_COLS.map((c) => (
                <th key={c.key} className="px-4 py-2 font-medium">
                  {c.label}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {manifests.map((m) => (
              <tr
                key={m.name}
                className="border-t border-functional-border-divider align-top"
              >
                <td className="primary-body4 px-4 py-2 font-semibold">
                  {m.match.version}
                </td>
                {MODULE_COLS.map((c) => {
                  const entries = m.modules?.[c.key]
                  return (
                    <td key={c.key} className="px-4 py-2">
                      {!entries || entries.length === 0 ? (
                        <span className="secondary-body5 text-functional-text-light">
                          not constrained
                        </span>
                      ) : (
                        <div className="flex flex-col gap-y-1">
                          {entries.map((e) => (
                            <div
                              key={e.version}
                              className="flex items-center gap-x-2"
                            >
                              <span className="primary-body4 font-mono">
                                {e.version}
                              </span>
                              <span
                                title={
                                  [e.reason, e.link].filter(Boolean).join(' ') ||
                                  undefined
                                }
                              >
                                <CosTag
                                  variant={STATUS_TAG[e.status]?.variant ?? 'stroke'}
                                  color={STATUS_TAG[e.status]?.color ?? 'default'}
                                >
                                  {e.status}
                                </CosTag>
                              </span>
                            </div>
                          ))}
                        </div>
                      )}
                    </td>
                  )
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
