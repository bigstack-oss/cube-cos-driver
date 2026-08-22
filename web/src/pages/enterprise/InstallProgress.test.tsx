// @vitest-environment jsdom
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { Install } from '../../api/enterprise'
import { InstallProgress } from './InstallProgress'

const fetchMock = vi.fn()
beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

const baseInstall = (overrides: Partial<Install>): Install => ({
  ClusterID: 'c1',
  Module: 'cmp',
  StartedAt: '2026-08-08T00:00:00Z',
  Manual: false,
  ManualStep: 0,
  SimulateAirgap: false,
  Params: {
    Project: 'demo',
    PublicNet: 'public',
    MgmtNet: 'public',
    LBIP: '10.32.36.120',
    OSImage: 'rancher.raw',
    Framework: 'demo',
    AppFile: 'cube-portal.pigz',
    FsImage: 'manila.qcow2',
    LBImage: 'amphora.qcow2',
    AdvisorFile: '',
    AdvisorLBIP: '',
    StorageBackend: 'CubeStorage',
  },
  Steps: [
    { Name: 'detect', Title: 'Detect framework', State: 'done', Output: '', Err: '' },
    { Name: 'import', Title: 'Import images', State: 'done', Output: 'ok', Err: '' },
  ],
  Current: 1,
  State: 'running',
  Portal: '',
  ...overrides,
})

describe('InstallProgress', () => {
  it('renders the completion card with a clickable portal link', () => {
    const install = baseInstall({
      State: 'done',
      Portal: 'http://10.32.36.120',
      Module: 'cmp',
    })
    render(
      <InstallProgress clusterId="c1" module="cmp" install={install} onClose={vi.fn()} />,
    )

    expect(screen.getByText(/✅ CubeCMP installed/)).toBeTruthy()
    const link = screen.getByRole('link', { name: 'http://10.32.36.120' })
    expect(link.getAttribute('href')).toBe('http://10.32.36.120')
    expect(link.getAttribute('target')).toBe('_blank')
  })

  it('renders the completion card with a clickable advisor link', () => {
    const install = baseInstall({
      State: 'done',
      Module: 'advisor',
      Portal: 'http://10.32.36.121/',
    })
    render(
      <InstallProgress clusterId="c1" module="advisor" install={install} onClose={vi.fn()} />,
    )

    expect(screen.getByText(/✅ Cube AI Advisor installed/)).toBeTruthy()
    const link = screen.getByRole('link', { name: 'http://10.32.36.121/' })
    expect(link.getAttribute('href')).toBe('http://10.32.36.121/')
    expect(link.getAttribute('target')).toBe('_blank')
  })

  it('renders the completion card without a portal link for appfw', () => {
    const install = baseInstall({
      State: 'done',
      Module: 'appfw',
      Portal: '',
    })
    render(
      <InstallProgress clusterId="c1" module="appfw" install={install} onClose={vi.fn()} />,
    )

    expect(screen.getByText(/✅ App-Framework installed/)).toBeTruthy()
    expect(screen.queryByRole('link')).toBeNull()
  })

  it('calls nextStep when Next is clicked in a manual running install', async () => {
    const user = userEvent.setup()
    const updated = baseInstall({
      State: 'done',
      Manual: true,
      Portal: 'http://10.32.36.120',
    })
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (typeof url === 'string' && url.includes('/enterprise/install/step/next')) {
        expect(init?.method).toBe('POST')
        return new Response(JSON.stringify(updated), { status: 200 })
      }
      return new Response(JSON.stringify(updated), { status: 200 })
    })

    const install = baseInstall({
      State: 'running',
      Manual: true,
      Steps: [
        { Name: 'detect', Title: 'Detect framework', State: 'done', Output: '', Err: '' },
        { Name: 'import', Title: 'Import images', State: 'pending', Output: '', Err: '' },
      ],
      Current: 1,
    })
    render(
      <InstallProgress clusterId="c1" module="cmp" install={install} onClose={vi.fn()} />,
    )

    await user.click(screen.getByRole('button', { name: 'Next' }))

    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        (c) =>
          typeof c[0] === 'string' &&
          (c[0] as string).includes('/enterprise/install/step/next') &&
          (c[1] as RequestInit | undefined)?.method === 'POST',
      )
      expect(posted).toBeTruthy()
    })
    // Terminal state after Next stops the poll, so no leaked interval.
    await waitFor(() => expect(screen.getByText(/✅ CubeCMP installed/)).toBeTruthy())
  })

  it('shows the errored step message on failure', () => {
    const install = baseInstall({
      State: 'error',
      Steps: [
        { Name: 'detect', Title: 'Detect framework', State: 'done', Output: '', Err: '' },
        { Name: 'import', Title: 'Import images', State: 'error', Output: '', Err: 'scp failed' },
      ],
      Current: 1,
    })
    render(
      <InstallProgress clusterId="c1" module="cmp" install={install} onClose={vi.fn()} />,
    )
    expect(screen.getByText('scp failed')).toBeTruthy()
  })

  it('shows a cancelled message', () => {
    const install = baseInstall({ State: 'cancelled' })
    render(
      <InstallProgress clusterId="c1" module="cmp" install={install} onClose={vi.fn()} />,
    )
    expect(screen.getByText('Installation cancelled')).toBeTruthy()
  })
})
