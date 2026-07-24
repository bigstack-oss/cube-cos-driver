import { describe, expect, it } from 'vitest'
import { IF } from '../../../../model/types'
import {
  changeIF,
  createBond,
  createVlan,
  IFDraft,
  isIFSelectable,
  pruneRoleSettings,
  removeIFs,
  resizeInitIFs,
} from '../nodeDraft'

let counter = 0
const makeId = () => `id-${++counter}`

const initIF = (id: string, name: string, extra?: Partial<IF>): IF => ({
  id,
  type: 'init',
  name,
  enabled: false,
  ...extra,
})

const baseDraft = (): IFDraft => ({
  initIFs: [
    initIF('a', 'IF.1', { enabled: true, IPAddr: '10.0.0.1' }),
    initIF('b', 'IF.2'),
    initIF('c', 'IF.3'),
  ],
  bondIFs: [],
  vlanIFs: [],
})

describe('resizeInitIFs', () => {
  it('grows with sequential names', () => {
    const d = resizeInitIFs(baseDraft(), 5, makeId)
    expect(d.initIFs).toHaveLength(5)
    expect(d.initIFs[4].name).toBe('IF.5')
  })
  it('shrinks and cascades removals', () => {
    const d = resizeInitIFs(baseDraft(), 1, makeId)
    expect(d.initIFs.map((f) => f.name)).toEqual(['IF.1'])
  })
})

describe('createBond', () => {
  it('enslaves members, clears their IPs, names bond from pool', () => {
    const d = createBond(baseDraft(), ['b', 'c'], 'bond-1')
    expect(d.bondIFs[0].name).toBe('mgmt')
    expect(d.bondIFs[0].slaves).toEqual(['b', 'c'])
    const slave = d.initIFs.find((f) => f.id === 'b')!
    expect(slave.master).toBe('bond-1')
    expect(slave.enabled).toBe(true)
    expect(slave.IPAddr).toBeUndefined()
  })
})

describe('createVlan / changeIF', () => {
  it('creates vlan named parent.tag and renames with bond', () => {
    let d = createBond(baseDraft(), ['b', 'c'], 'bond-1')
    d = createVlan(d, d.bondIFs[0], 'vlan-1')
    expect(d.vlanIFs[0].name).toBe('mgmt.1')

    d = changeIF(d, d.bondIFs[0], { name: 'data' })
    expect(d.vlanIFs[0].name).toBe('data.1')
  })
})

describe('removeIFs cascades', () => {
  it('removes bond when all slaves removed, and vlans on it', () => {
    let d = createBond(baseDraft(), ['b', 'c'], 'bond-1')
    d = createVlan(d, d.bondIFs[0], 'vlan-1')
    d = removeIFs(d, ['b', 'c'])
    expect(d.bondIFs).toHaveLength(0)
    expect(d.vlanIFs).toHaveLength(0)
    expect(d.initIFs).toHaveLength(1)
  })
})

describe('isIFSelectable', () => {
  it('requires enabled, non-slave, IP for mgmt/default', () => {
    const enabledWithIP = initIF('x', 'IF.9', {
      enabled: true,
      IPAddr: '1.2.3.4',
    })
    const enabledNoIP = initIF('y', 'IF.10', { enabled: true })
    const slave = initIF('z', 'IF.11', { enabled: true, master: 'bond-9' })
    expect(isIFSelectable(enabledWithIP, 'mgmtIF')).toBe(true)
    expect(isIFSelectable(enabledNoIP, 'mgmtIF')).toBe(false)
    expect(isIFSelectable(enabledNoIP, 'providerIF')).toBe(true)
    expect(isIFSelectable(slave, 'providerIF')).toBe(false)
    expect(isIFSelectable(undefined, 'mgmtIF')).toBe(false)
  })
})

describe('pruneRoleSettings', () => {
  it('clears selections that became invalid', () => {
    const d = baseDraft()
    const settings = pruneRoleSettings(d, 'control', {
      mgmtIF: { id: 'a', type: 'init' },
      storIF: { id: 'b', type: 'init' }, // IF.2 disabled → invalid
      storIFBackend: {},
    })
    expect(settings.mgmtIF.id).toBe('a')
    expect(settings.storIF).toEqual({})
  })
})
