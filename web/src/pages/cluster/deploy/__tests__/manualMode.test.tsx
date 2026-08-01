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

describe('DeployModal manual toggle', () => {
  it('sends manual=true when the toggle is checked', async () => {
    const user = userEvent.setup()
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (typeof url === 'string' && url.endsWith('/deploy/plan')) {
        return new Response(
          JSON.stringify({
            allAssigned: true,
            nodes: [{ hostname: 'cube-1', assigned: true, machineLabel: 'm', bmcAddress: 'x', osDisk: 'sda' }],
          }),
          { status: 200 },
        )
      }
      if (init?.method === 'POST') return new Response('', { status: 202 })
      return new Response('{}', { status: 200 })
    })
    render(<DeployModal isOpen clusterId="c1" onCancel={() => {}} onStarted={() => {}} />)
    await waitFor(() => expect(screen.getByText(/Manual \(step-by-step\)/)).toBeTruthy())
    await user.click(screen.getByRole('checkbox', { name: /Manual/ }))
    await user.click(screen.getByRole('button', { name: 'Power on & deploy' }))
    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        (c) =>
          (c[1] as RequestInit | undefined)?.method === 'POST' &&
          typeof c[0] === 'string' &&
          (c[0] as string).endsWith('/deploy'),
      )
      expect(posted).toBeTruthy()
      expect(JSON.parse((posted![1] as RequestInit).body as string).manual).toBe(true)
    })
  })
})

const manualDeploy = (step: number): Deploy => ({
  clusterId: 'c1',
  startedAt: '',
  manual: true,
  manualStep: step,
  canAdvance: true,
  nodes: {
    'cube-1': { hostname: 'cube-1', machineId: 'm', state: 'preflight-ok', updatedAt: '', light1: 'green', light2: 'off', phase: 'preflight-net' },
  },
})

describe('DeployProgress manual step bar', () => {
  it('highlights the current step and Next advances the gate', async () => {
    const user = userEvent.setup()
    let step = 1
    fetchMock.mockImplementation(async (url: string) => {
      if (typeof url === 'string' && url.endsWith('/step/next')) {
        step = 2
        return new Response('{"manualStep":2}', { status: 200 })
      }
      // status endpoint
      return new Response(JSON.stringify(manualDeploy(step)), { status: 200 })
    })
    render(<DeployProgress clusterId="c1" />)
    await waitFor(() => expect(screen.getByText(/1\. Preflight check/)).toBeTruthy())
    await user.click(screen.getByRole('button', { name: 'Next' }))
    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        (c) => typeof c[0] === 'string' && (c[0] as string).endsWith('/step/next'),
      )
      expect(posted).toBeTruthy()
    })
  })

  it('disables Next until the step is complete (canAdvance false)', async () => {
    fetchMock.mockImplementation(
      async () =>
        new Response(
          JSON.stringify({ ...manualDeploy(1), canAdvance: false }),
          { status: 200 },
        ),
    )
    render(<DeployProgress clusterId="c1" />)
    await waitFor(() => expect(screen.getByText(/1\. Preflight check/)).toBeTruthy())
    expect(
      screen.getByRole('button', { name: 'Next' }).hasAttribute('disabled'),
    ).toBe(true)
  })
})
