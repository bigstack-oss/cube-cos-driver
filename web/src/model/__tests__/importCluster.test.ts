import { describe, expect, it } from 'vitest'
import { ha3 } from '../../testing/fixtures'
import { checkClusterDetailValid } from '../importCluster'

const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T

describe('checkClusterDetailValid', () => {
  it('accepts the ha3 fixture', () => {
    const res = checkClusterDetailValid(clone(ha3))
    expect(res.ok).toBe(true)
    if (res.ok) {
      expect(res.detail.clusterInfo.name).toBe('sky-lab')
    }
  })

  it('rejects non-objects', () => {
    expect(checkClusterDetailValid('nope').ok).toBe(false)
    expect(checkClusterDetailValid(null).ok).toBe(false)
  })

  it('rejects missing clusterInfo id', () => {
    const d = clone<Record<string, unknown>>(ha3 as never)
    delete (d.clusterInfo as Record<string, unknown>).id
    const res = checkClusterDetailValid(d)
    expect(res.ok).toBe(false)
    if (!res.ok) expect(res.message).toContain('id of clusterInfo')
  })

  it('rejects HASettings present when HA disabled', () => {
    const d = clone(ha3) as never as Record<string, never>
    const config = d.clusterConfig as Record<string, unknown>
    config.HA = false
    const res = checkClusterDetailValid(d)
    expect(res.ok).toBe(false)
  })

  it('rejects malformed interface entries', () => {
    const d = clone(ha3)
    ;(d.nodeData[0].initIFs[0] as Record<string, unknown>).enabled = 'yes'
    const res = checkClusterDetailValid(d)
    expect(res.ok).toBe(false)
    if (!res.ok) expect(res.message).toContain('enabled')
  })
})
