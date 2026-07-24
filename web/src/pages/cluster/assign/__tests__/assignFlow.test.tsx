import { describe, expect, it } from 'vitest'
import { NodeConfig } from '../../../../model/types'
import { assembleNode, buildInitIFs } from '../AssignServerFlow'
import { NicMapperValue } from '../NicMapper'

const baseNode = (): NodeConfig => ({
  id: 'n1',
  hostname: 'cube-1',
  // Prior config carries an IP on IF.1 that must be preserved by label.
  initIFs: [
    {
      id: 'old-0',
      type: 'init',
      name: 'IF.1',
      enabled: true,
      IPAddr: '10.0.0.5',
      IPMask: '255.255.0.0',
    },
    { id: 'old-1', type: 'init', name: 'IF.2', enabled: false },
  ],
  bondIFs: [],
  vlanIFs: [],
  defaultIF: {},
  defaultGateway: '10.0.0.254',
  role: 'storage', // requires mgmtIF + storIF
  roleSettings: { mgmtIF: {}, storIF: {}, storIFBackend: {} },
})

describe('buildInitIFs', () => {
  it('labels ports IF.1..IF.N positionally', () => {
    const ifs = buildInitIFs([{ mac: 'a' }, { mac: 'b' }, { mac: 'c' }])
    expect(ifs.map((f) => f.name)).toEqual(['IF.1', 'IF.2', 'IF.3'])
    expect(ifs.map((f) => f.id)).toEqual(['if-0', 'if-1', 'if-2'])
  })
})

describe('assembleNode', () => {
  it('rewrites topology + role tags and preserves IP by label', () => {
    const mapper: NicMapperValue = {
      draft: {
        initIFs: buildInitIFs([{ mac: 'a' }, { mac: 'b' }]),
        bondIFs: [],
        vlanIFs: [],
      },
      roleTags: { mgmtIF: 'if-0', storIF: 'if-1' },
    }
    const result = assembleNode(baseNode(), mapper)

    expect(result.initIFs.map((f) => f.name)).toEqual(['IF.1', 'IF.2'])
    // IF.1 keeps its prior IP; role tags resolve to the new interface ids.
    expect(result.initIFs[0].IPAddr).toBe('10.0.0.5')
    expect(result.roleSettings.mgmtIF.id).toBe('if-0')
    expect(result.roleSettings.storIF.id).toBe('if-1')
    // Default interface follows management.
    expect(result.defaultIF.id).toBe('if-0')
  })

  it('handles a bond tagged to a role', () => {
    const mapper: NicMapperValue = {
      draft: {
        initIFs: [
          { id: 'if-0', type: 'init', name: 'IF.1', enabled: true, master: 'b0' },
          { id: 'if-1', type: 'init', name: 'IF.2', enabled: true, master: 'b0' },
        ],
        bondIFs: [
          {
            id: 'b0',
            type: 'bond',
            name: 'bond0',
            enabled: true,
            slaves: ['if-0', 'if-1'],
          },
        ],
        vlanIFs: [],
      },
      roleTags: { mgmtIF: 'b0', storIF: 'b0' },
    }
    const result = assembleNode(baseNode(), mapper)
    expect(result.roleSettings.mgmtIF).toEqual({ id: 'b0', type: 'bond' })
    expect(result.bondIFs[0].name).toBe('bond0')
  })
})
