// Cluster target selector for enterprise install/uninstall: pick a configured
// cluster from the dropdown, or enter a cluster VIP directly (an ad-hoc target
// the driver doesn't have stored). Both resolve to a host the driver SSHes to.
import { ClusterDigest } from '../../model/types'

export type Target = { mode: 'cluster' | 'vip'; clusterId: string; vip: string }

export const emptyTarget = (clusterId = ''): Target => ({
  mode: 'cluster',
  clusterId,
  vip: '',
})

// targetId is the run identifier (configured cluster id, or the VIP for an
// ad-hoc target); targetVip is the explicit VIP sent to the backend ('' = use
// the configured cluster).
export const targetId = (t: Target): string =>
  t.mode === 'vip' ? t.vip.trim() : t.clusterId
export const targetVip = (t: Target): string =>
  t.mode === 'vip' ? t.vip.trim() : ''

export function ClusterTargetPicker({
  clusters,
  value,
  onChange,
  disabled,
}: {
  clusters: ClusterDigest[]
  value: Target
  onChange: (t: Target) => void
  disabled?: boolean
}) {
  const inputClass =
    'primary-body4 rounded-md border border-functional-border-divider px-3 py-2 outline-none focus:border-primary disabled:cursor-not-allowed disabled:bg-functional-hover-grey'
  return (
    <div className="flex flex-col gap-y-2">
      <span className="secondary-body5 font-medium text-functional-text-secondary">
        Cluster
      </span>
      <div className="flex gap-x-4">
        <label className="secondary-body5 flex items-center gap-x-1">
          <input
            type="radio"
            checked={value.mode === 'cluster'}
            disabled={disabled}
            onChange={() => onChange({ ...value, mode: 'cluster' })}
          />
          Configured cluster
        </label>
        <label className="secondary-body5 flex items-center gap-x-1">
          <input
            type="radio"
            checked={value.mode === 'vip'}
            disabled={disabled}
            onChange={() => onChange({ ...value, mode: 'vip' })}
          />
          Enter VIP
        </label>
      </div>
      {value.mode === 'cluster' ? (
        <select
          className={inputClass}
          value={value.clusterId}
          disabled={disabled}
          onChange={(e) => onChange({ ...value, clusterId: e.target.value })}
        >
          <option value="">Select a cluster…</option>
          {clusters.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
      ) : (
        <input
          className={inputClass}
          value={value.vip}
          placeholder="cluster VIP, e.g. 10.32.10.140"
          disabled={disabled}
          onChange={(e) => onChange({ ...value, vip: e.target.value })}
        />
      )}
    </div>
  )
}
