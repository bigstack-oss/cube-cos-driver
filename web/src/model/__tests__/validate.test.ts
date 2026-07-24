import { describe, expect, it } from 'vitest'
import { ha3 } from '../../testing/fixtures'
import {
  findDuplicatedHostname,
  findDuplicatedIF,
  isDanger,
  isRoleDistributionValid,
  isValidCIDR,
  isValidIPv4,
  sanitizeClusterName,
  validateCluster,
} from '../validate'

const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T

describe('sanitizeClusterName', () => {
  it('strips leading dots, slashes and special chars', () => {
    expect(sanitizeClusterName('..a/b?c', 'fallback')).toBe('ab_c')
  })
  it('falls back on blank', () => {
    expect(sanitizeClusterName('   ', 'Cluster 1')).toBe('Cluster 1')
  })
  it('caps at 20 chars', () => {
    expect(sanitizeClusterName('x'.repeat(30), 'f')).toHaveLength(20)
  })
})

describe('ip validators', () => {
  it('accepts valid IPv4', () => {
    expect(isValidIPv4('10.254.0.1')).toBe(true)
    expect(isValidIPv4('256.0.0.1')).toBe(false)
    expect(isValidIPv4('1.2.3')).toBe(false)
  })
  it('accepts valid CIDR', () => {
    expect(isValidCIDR('10.254.0.0/16')).toBe(true)
    expect(isValidCIDR('10.254.0.0/33')).toBe(false)
    expect(isValidCIDR('10.254.0.0')).toBe(false)
  })
})

describe('validateCluster on ha3 fixture', () => {
  it('has no errors', () => {
    const d = clone(ha3)
    expect(isDanger(d.clusterConfig, d.nodeData)).toBe(false)
    const errors = validateCluster(d.clusterConfig, d.nodeData).filter(
      (p) => p.level === 'error',
    )
    expect(errors).toEqual([])
  })

  it('flags duplicate hostname', () => {
    const d = clone(ha3)
    d.nodeData[1].hostname = d.nodeData[0].hostname
    expect(findDuplicatedHostname(d.nodeData)).toBe('cube-1')
    expect(isDanger(d.clusterConfig, d.nodeData)).toBe(true)
  })

  it('flags duplicate IF IP', () => {
    const d = clone(ha3)
    d.nodeData[1].initIFs[0].IPAddr = d.nodeData[0].initIFs[0].IPAddr
    expect(findDuplicatedIF(d.nodeData)?.ipAddr).toBe('10.254.0.1')
  })

  it('flags virtual hostname collision', () => {
    const d = clone(ha3)
    d.nodeData[0].hostname = d.clusterConfig.HASettings.virtualHostname!
    expect(isDanger(d.clusterConfig, d.nodeData)).toBe(true)
  })

  it('flags bad role distribution', () => {
    const d = clone(ha3)
    d.nodeData.pop() // 2 control-converged in HA is invalid
    expect(isRoleDistributionValid(d.clusterConfig, d.nodeData)).toBe(false)
  })

  it('flags unresolved role interface', () => {
    const d = clone(ha3)
    d.nodeData[0].roleSettings.mgmtIF = { id: 'nope' }
    expect(isDanger(d.clusterConfig, d.nodeData)).toBe(true)
  })
})
