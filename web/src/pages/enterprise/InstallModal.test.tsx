// @vitest-environment jsdom
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { InstallModal } from './InstallModal'

const fetchMock = vi.fn()

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

const artifacts = {
  AppFW: [
    'rancher-cluster-image-rke2-v1.32.4.raw',
    'manila-service-image-yoga.qcow2',
    'amphora-x64-haproxy-yoga.qcow2',
  ],
  CMP: ['cube-portal-2.1.0.pigz'],
}

// Shared fetch stub for clusters/cluster-detail/artifacts/install-POST.
const stubFetch = () => {
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url === '/api/v1/clusters') {
      return new Response(
        JSON.stringify([{ id: 'aabbccddee01', name: 'sky-lab', nodes: [] }]),
        { status: 200 },
      )
    }
    if (url === '/api/v1/clusters/aabbccddee01') {
      return new Response(
        JSON.stringify({
          clusterInfo: { id: 'aabbccddee01', name: 'sky-lab' },
          clusterConfig: { HASettings: { virtualIP: '10.32.10.140' } },
          nodeData: [],
        }),
        { status: 200 },
      )
    }
    if (url === '/api/v1/enterprise/artifacts') {
      return new Response(JSON.stringify(artifacts), { status: 200 })
    }
    if (url.endsWith('/enterprise/cluster-info') && init?.method === 'POST') {
      return new Response(
        JSON.stringify({
          projects: ['admin'],
          networks: ['public'],
          airgapSupported: false,
          suggestedLBIP: '',
          suggestedStorage: 'CubeStorage',
          version: '3.1.0',
          manifest: '',
          manifests: [],
        }),
        { status: 200 },
      )
    }
    // No install in flight (reattach probe): 404.
    if (url.includes('/enterprise/install?') && (!init || init.method === undefined)) {
      return new Response('not found', { status: 404 })
    }
    if (url.endsWith('/enterprise/install') && init?.method === 'POST') {
      const params = JSON.parse(init.body as string).params
      return new Response(
        JSON.stringify({
          ClusterID: 'aabbccddee01',
          Module: 'cmp',
          StartedAt: '',
          Manual: false,
          ManualStep: 0,
          SimulateAirgap: false,
          Params: params,
          Steps: [],
          Current: 0,
          State: 'running',
          Portal: '',
        }),
        { status: 200 },
      )
    }
    return new Response('{}', { status: 200 })
  })
}

describe('InstallModal', () => {
  it('shows CMP-only fields and posts PascalCase params to startInstall', async () => {
    const user = userEvent.setup()
    stubFetch()

    render(
      <MemoryRouter>
        <InstallModal module="cmp" onClose={vi.fn()} />
      </MemoryRouter>,
    )

    // CMP-only fields present.
    expect(screen.getByLabelText('Framework')).toBeTruthy()
    expect(screen.getByLabelText('.pigz package')).toBeTruthy()

    await waitFor(() => expect(screen.getByText('sky-lab')).toBeTruthy())
    await user.selectOptions(screen.getByLabelText('Cluster'), 'aabbccddee01')

    // VIP-derived password placeholder.
    await waitFor(() =>
      expect(screen.getByLabelText('Password').getAttribute('placeholder')).toBe(
        'Cube@10.140',
      ),
    )

    await user.clear(screen.getByLabelText('Framework'))
    await user.type(screen.getByLabelText('Framework'), 'demo-project')
    await user.type(screen.getByLabelText('LB IP'), '10.32.36.120')

    await waitFor(() =>
      expect(
        screen.getByText('rancher-cluster-image-rke2-v1.32.4.raw'),
      ).toBeTruthy(),
    )
    await user.selectOptions(
      screen.getByLabelText('OS image'),
      'rancher-cluster-image-rke2-v1.32.4.raw',
    )
    await user.selectOptions(
      screen.getByLabelText('.pigz package'),
      'cube-portal-2.1.0.pigz',
    )

    await user.click(screen.getByRole('button', { name: 'Install' }))

    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        (c) =>
          typeof c[0] === 'string' &&
          (c[0] as string).endsWith('/enterprise/install') &&
          (c[1] as RequestInit | undefined)?.method === 'POST',
      )
      expect(posted).toBeTruthy()
      const body = JSON.parse((posted![1] as RequestInit).body as string)
      expect(body.module).toBe('cmp')
      expect(body.password).toBe('')
      expect(body.params.Project).toBe('demo-project')
      expect(body.params.PublicNet).toBe('public')
      expect(body.params.MgmtNet).toBe('public')
      expect(body.params.LBIP).toBe('10.32.36.120')
      expect(body.params.OSImage).toMatch(/\.raw$/)
      expect(body.params.AppFile).toBe('cube-portal-2.1.0.pigz')
      // Framework defaults to the Project value (no framework_list API exists).
      expect(body.params.Framework).toBe('demo-project')
      expect(body.params.FsImage).toBe('manila-service-image-yoga.qcow2')
      expect(body.params.LBImage).toBe('amphora-x64-haproxy-yoga.qcow2')
    })

    await waitFor(() =>
      expect(screen.getByLabelText('install progress')).toBeTruthy(),
    )
  })

  it('keeps Install disabled for cmp until a .pigz is selected', async () => {
    const user = userEvent.setup()
    stubFetch()

    render(
      <MemoryRouter>
        <InstallModal module="cmp" onClose={vi.fn()} />
      </MemoryRouter>,
    )

    await waitFor(() => expect(screen.getByText('sky-lab')).toBeTruthy())
    await user.selectOptions(screen.getByLabelText('Cluster'), 'aabbccddee01')
    await user.clear(screen.getByLabelText('Framework'))
    await user.type(screen.getByLabelText('Framework'), 'demo-project')
    await user.type(screen.getByLabelText('LB IP'), '10.32.36.120')
    await waitFor(() =>
      expect(
        screen.getByText('rancher-cluster-image-rke2-v1.32.4.raw'),
      ).toBeTruthy(),
    )
    await user.selectOptions(
      screen.getByLabelText('OS image'),
      'rancher-cluster-image-rke2-v1.32.4.raw',
    )

    // Cluster + Project + LB IP + OS image filled, but no .pigz selected yet.
    expect(
      screen.getByRole('button', { name: 'Install' }).hasAttribute('disabled'),
    ).toBe(true)

    await user.selectOptions(
      screen.getByLabelText('.pigz package'),
      'cube-portal-2.1.0.pigz',
    )

    expect(
      screen.getByRole('button', { name: 'Install' }).hasAttribute('disabled'),
    ).toBe(false)
  })
})
