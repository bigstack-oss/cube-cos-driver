// @vitest-environment jsdom
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { Deploy } from '../../../../api/deploy'
import { DeployModal } from '../DeployModal'
import { DeployProgress } from '../DeployProgress'

const fetchMock = vi.fn()
beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

describe('DeployModal', () => {
  it('shows the plan and starts a deploy on confirm', async () => {
    const user = userEvent.setup()
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (typeof url === 'string' && url.endsWith('/deploy/plan')) {
        return new Response(
          JSON.stringify({
            allAssigned: true,
            nodes: [
              { hostname: 'cube-1', assigned: true, machineLabel: 'sky141', bmcAddress: '10.0.0.1', osDisk: 'sda', macs: ['aa:01'] },
            ],
          }),
          { status: 200 },
        )
      }
      if (init?.method === 'POST') return new Response('', { status: 202 })
      return new Response('{}', { status: 200 })
    })
    const onStarted = vi.fn()
    render(
      <DeployModal isOpen clusterId="c1" onCancel={() => {}} onStarted={onStarted} />,
    )
    await waitFor(() => expect(screen.getByText(/sky141/)).toBeTruthy())
    await user.click(screen.getByRole('button', { name: 'Power on & deploy' }))
    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        (c) => (c[1] as RequestInit | undefined)?.method === 'POST',
      )
      expect(posted).toBeTruthy()
      const body = JSON.parse((posted![1] as RequestInit).body as string)
      expect(body.confirm).toBe(true)
    })
    expect(onStarted).toHaveBeenCalled()
  })

  it('blocks deploy when not all nodes assigned', async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({
          allAssigned: false,
          nodes: [
            { hostname: 'cube-1', assigned: true, machineLabel: 'm', bmcAddress: 'x', osDisk: 'sda' },
            { hostname: 'cube-2', assigned: false },
          ],
        }),
        { status: 409 },
      ),
    )
    render(<DeployModal isOpen clusterId="c1" onCancel={() => {}} onStarted={() => {}} />)
    await waitFor(() => expect(screen.getByText(/Not all nodes assigned/)).toBeTruthy())
    expect(
      screen.getByRole('button', { name: 'Power on & deploy' }).hasAttribute('disabled'),
    ).toBe(true)
  })
})

describe('DeployProgress', () => {
  it('renders per-node state, preflight, and error message', async () => {
    const deploy: Deploy = {
      clusterId: 'c1',
      startedAt: '2026-07-24T00:00:00Z',
      nodes: {
        'cube-1': {
          hostname: 'cube-1',
          machineId: 'm1',
          state: 'done',
          updatedAt: '',
          preflight: [
            { target: 'gateway 10.0.0.254', ok: true },
            { target: 'time sync', ok: true, detail: 'corrected skew 3s' },
          ],
        },
        'cube-2': {
          hostname: 'cube-2',
          machineId: 'm2',
          state: 'error',
          message: 'bmc unreachable',
          updatedAt: '',
        },
      },
    }
    fetchMock.mockResolvedValue(new Response(JSON.stringify(deploy), { status: 200 }))
    render(<DeployProgress clusterId="c1" />)
    await waitFor(() => expect(screen.getByText('cube-1')).toBeTruthy())
    expect(screen.getByText('done')).toBeTruthy()
    expect(screen.getByText('error')).toBeTruthy()
    expect(screen.getByText('bmc unreachable')).toBeTruthy()
    expect(screen.getByText(/gateway 10.0.0.254/)).toBeTruthy()
  })
})
