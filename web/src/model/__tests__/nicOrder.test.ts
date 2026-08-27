import { describe, expect, it } from 'vitest'
import { cosNics, NIC } from '../machine'

// qa37-r630: the inspect boot names ports in driver-probe order, which
// interleaves the two Mellanox and two Intel ports.
const qa37: NIC[] = [
  { name: 'eth0', mac: '24:8a:07:50:eb:48', pciAddr: '0000:01:00.0' },
  { name: 'eth1', mac: 'a0:36:9f:e4:08:64', pciAddr: '0000:04:00.0' },
  { name: 'eth2', mac: '24:8a:07:50:eb:49', pciAddr: '0000:01:00.1' },
  { name: 'eth3', mac: 'a0:36:9f:e4:08:66', pciAddr: '0000:04:00.1' },
]

describe('cosNics', () => {
  it('orders and labels by PCI bus, as CubeCOS does', () => {
    expect(cosNics(qa37).map((n) => [n.name, n.pciAddr])).toEqual([
      ['eth0', '0000:01:00.0'],
      ['eth1', '0000:01:00.1'],
      ['eth2', '0000:04:00.0'],
      ['eth3', '0000:04:00.1'],
    ])
  })

  it('keeps each port with its own MAC', () => {
    expect(cosNics(qa37).map((n) => n.mac)).toEqual([
      '24:8a:07:50:eb:48',
      '24:8a:07:50:eb:49',
      'a0:36:9f:e4:08:64',
      'a0:36:9f:e4:08:66',
    ])
  })

  it('sorts hex bus ids numerically, not lexically', () => {
    const nics: NIC[] = [
      { name: 'a', pciAddr: '0000:1a:00.0' },
      { name: 'b', pciAddr: '0000:09:00.0' },
      { name: 'c', pciAddr: '0001:00:00.0' },
    ]
    expect(cosNics(nics).map((n) => n.pciAddr)).toEqual([
      '0000:09:00.0',
      '0000:1a:00.0',
      '0001:00:00.0',
    ])
  })

  it('leaves inventory without PCI addresses untouched', () => {
    const nics: NIC[] = [
      { name: 'NIC.Integrated.1-2', mac: 'aa:bb:cc:dd:ee:02' },
      { name: 'NIC.Integrated.1-1', mac: 'aa:bb:cc:dd:ee:01' },
    ]
    expect(cosNics(nics)).toEqual(nics)
  })

  it('handles empty/undefined', () => {
    expect(cosNics(undefined)).toEqual([])
    expect(cosNics([])).toEqual([])
  })
})
