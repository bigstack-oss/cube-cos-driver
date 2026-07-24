// @vitest-environment jsdom
import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { NodeConfig } from '../../../../model/types'
import { SnapshotNetworkMapper } from '../SnapshotNetworkMapper'

const node: NodeConfig = {
  id: 'n1',
  hostname: 'sky141',
  initIFs: [
    { id: 'p1', type: 'init', name: 'IF.1', enabled: true, IPAddr: '10.254.0.1' },
    { id: 'p5', type: 'init', name: 'IF.4', enabled: true, IPAddr: '10.253.0.1' },
  ],
  bondIFs: [],
  vlanIFs: [
    { id: 'v80', type: 'vlan', name: 'IF.4.80', master: 'p5', enabled: true, IPAddr: '10.80.0.1' },
  ],
  defaultIF: { id: 'p1', type: 'init' },
  defaultGateway: '10.254.0.254',
  role: 'control-converged',
  roleSettings: {
    mgmtIF: { id: 'p1', type: 'init' },
    providerIF: { id: 'p5', type: 'init' },
    overlayIF: { id: 'v80', type: 'vlan' },
    storIF: { id: 'p5', type: 'init' },
    storIFBackend: {},
  },
}

describe('SnapshotNetworkMapper', () => {
  it('presets from the snapshot: IF table, base interfaces, roles with IPs', () => {
    render(
      <SnapshotNetworkMapper
        node={node}
        ports={[
          { name: 'eth0', mac: 'aa:01' },
          { name: 'eth1', mac: 'aa:02' },
        ]}
        onChange={vi.fn()}
        onReorderPort={vi.fn()}
      />,
    )
    // IF Table (from BMC) labels + a role with its IP.
    expect(screen.getByText(/From BMC/)).toBeTruthy()
    expect(screen.getByText('Management interface')).toBeTruthy()
    expect(screen.getByText(/IP 10.254.0.1/)).toBeTruthy()
    // VLAN label surfaced on the overlay role line context.
    expect(screen.getAllByText(/IF.4.80/).length).toBeGreaterThan(0)
  })
})
