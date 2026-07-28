import { describe, expect, it } from 'vitest'
import { NodeConfig } from '../../../../model/types'
import { baseGroups, baseInterfaces, bindPort, buildChains } from '../snapshotTopology'

// sky-like node: IF.1 = mgmt (plain); IF.5 carries provider (untagged) +
// vlan80 -> overlay + vlan91 -> storage.
const skyNode = (): NodeConfig => ({
  id: 'n1',
  hostname: 'sky141',
  initIFs: [
    { id: 'p1', type: 'init', name: 'IF.1', enabled: true, IPAddr: '10.254.0.1', IPMask: '255.255.0.0' },
    { id: 'p5', type: 'init', name: 'IF.4', enabled: true, IPAddr: '10.253.0.1', IPMask: '255.255.0.0' },
  ],
  bondIFs: [],
  vlanIFs: [
    { id: 'v80', type: 'vlan', name: 'IF.4.80', master: 'p5', enabled: true, IPAddr: '10.80.0.1', IPMask: '255.255.0.0' },
    { id: 'v91', type: 'vlan', name: 'IF.4.91', master: 'p5', enabled: true, IPAddr: '10.91.0.1', IPMask: '255.255.0.0' },
  ],
  defaultIF: { id: 'p1', type: 'init' },
  defaultGateway: '10.254.0.254',
  role: 'control-converged',
  roleSettings: {
    mgmtIF: { id: 'p1', type: 'init' },
    providerIF: { id: 'p5', type: 'init' },
    overlayIF: { id: 'v80', type: 'vlan' },
    storIF: { id: 'v91', type: 'vlan' },
    storIFBackend: {},
  },
})

describe('buildChains', () => {
  it('resolves direct, untagged, and VLAN role chains with IPs', () => {
    const chains = buildChains(skyNode())
    const byRole = Object.fromEntries(chains.map((c) => [c.role, c]))

    // mgmt: plain IF.1 directly, no vlan/bond.
    expect(byRole.mgmtIF.baseIf?.name).toBe('IF.1')
    expect(byRole.mgmtIF.vlan).toBeUndefined()
    expect(byRole.mgmtIF.ip).toBe('10.254.0.1')

    // provider: untagged on IF.4.
    expect(byRole.providerIF.baseIf?.name).toBe('IF.4')
    expect(byRole.providerIF.vlan).toBeUndefined()

    // overlay: vlan80 on IF.4.
    expect(byRole.overlayIF.vlan?.name).toBe('IF.4.80')
    expect(byRole.overlayIF.baseIf?.name).toBe('IF.4')
    expect(byRole.overlayIF.ip).toBe('10.80.0.1')
  })
})

describe('baseInterfaces', () => {
  it('returns the distinct physical bases (IF.1, IF.4)', () => {
    expect(baseInterfaces(skyNode()).map((f) => f.name).sort()).toEqual([
      'IF.1',
      'IF.4',
    ])
  })
})

describe('bindPort', () => {
  it('corrects IF.4 -> IF.5 and cascades VLAN labels', () => {
    // Real port at index 4 => IF.5.
    const fixed = bindPort(skyNode(), 'p5', 4)
    const p5 = fixed.initIFs.find((f) => f.id === 'p5')!
    expect(p5.name).toBe('IF.5')
    expect(fixed.vlanIFs.find((f) => f.id === 'v80')!.name).toBe('IF.5.80')
    expect(fixed.vlanIFs.find((f) => f.id === 'v91')!.name).toBe('IF.5.91')
    // IF.1 untouched.
    expect(fixed.initIFs.find((f) => f.id === 'p1')!.name).toBe('IF.1')
  })
})

// Bonded node: IF.5 + IF.6 are slaves of bond0; overlay rides vlan80 on the
// bond; provider is the bond untagged; mgmt stays plain IF.1.
const bondNode = (): NodeConfig => ({
  id: 'n2',
  hostname: 'sky142',
  initIFs: [
    { id: 'p1', type: 'init', name: 'IF.1', enabled: true, IPAddr: '10.254.0.2', IPMask: '255.255.0.0' },
    { id: 'p5', type: 'init', name: 'IF.5', enabled: true },
    { id: 'p6', type: 'init', name: 'IF.6', enabled: true },
  ],
  bondIFs: [
    { id: 'b0', type: 'bond', name: 'bond0', enabled: true, slaves: ['p5', 'p6'] },
  ],
  vlanIFs: [
    { id: 'v80', type: 'vlan', name: 'bond0.80', master: 'b0', enabled: true, IPAddr: '10.80.0.2', IPMask: '255.255.0.0' },
  ],
  defaultIF: { id: 'p1', type: 'init' },
  defaultGateway: '10.254.0.254',
  role: 'control-converged',
  roleSettings: {
    mgmtIF: { id: 'p1', type: 'init' },
    providerIF: { id: 'b0', type: 'bond' },
    overlayIF: { id: 'v80', type: 'vlan' },
    storIF: { id: 'v80', type: 'vlan' },
    storIFBackend: {},
  },
})

describe('bond slaves in the mapper', () => {
  it('every bond slave is a drop target, not just slaves[0]', () => {
    expect(baseInterfaces(bondNode()).map((f) => f.name).sort()).toEqual([
      'IF.1',
      'IF.5',
      'IF.6',
    ])
  })

  it('bond chains expose all slave bases', () => {
    const chains = buildChains(bondNode())
    const byRole = Object.fromEntries(chains.map((c) => [c.role, c]))
    expect(byRole.providerIF.bond?.name).toBe('bond0')
    expect((byRole.providerIF.bases ?? []).map((f) => f.name)).toEqual(['IF.5', 'IF.6'])
    expect((byRole.overlayIF.bases ?? []).map((f) => f.name)).toEqual(['IF.5', 'IF.6'])
  })

  it('bindPort relabels an individual slave', () => {
    const out = bindPort(bondNode(), 'p6', 6) // drop real port 7 onto IF.6
    const p6 = out.initIFs.find((f) => f.id === 'p6')
    expect(p6?.name).toBe('IF.7')
  })
})

describe('baseGroups', () => {
  it('groups bond slaves into one labeled block; plain bases stay singletons', () => {
    const groups = baseGroups(bondNode())
    expect(groups.map((g) => g.label ?? '-')).toEqual(['-', 'bond0'])
    expect(groups[0].bases.map((f) => f.name)).toEqual(['IF.1'])
    expect(groups[1].key).toBe('bond-b0')
    expect(groups[1].bases.map((f) => f.name)).toEqual(['IF.5', 'IF.6'])
  })

  it('non-bonded node has only unlabeled singletons', () => {
    const groups = baseGroups(skyNode())
    expect(groups.every((g) => !g.label)).toBe(true)
    expect(groups.flatMap((g) => g.bases.map((f) => f.name)).sort()).toEqual(['IF.1', 'IF.4'])
  })
})
