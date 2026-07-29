// @vitest-environment jsdom
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { Deploy } from '../../../../api/deploy'
import { PreflightReportModal } from '../PreflightReportModal'

const fetchMock = vi.fn()
beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

const deploy: Deploy = {
  clusterId: 'c1',
  startedAt: '2026-07-29T00:00:00Z',
  nodes: {
    'sky141': {
      hostname: 'sky141',
      machineId: 'm1',
      state: 'preflight-ok',
      updatedAt: '',
      light1: 'green',
      light2: 'off',
      phase: 'preflight-net',
      installerPreflight: {
        carrierOk: true,
        clockSkewSec: 0.12,
        passed: true,
        reportedAt: '2026-07-29T00:01:00Z',
        matrix: [
          { target: 'bond0 member IF.5 (eth4) link', ok: true, detail: 'car=1,op=up,spd=10000' },
          { target: 'gateway 10.32.0.254', ok: true, detail: 'reachable' },
        ],
      },
    },
    'sky142': {
      hostname: 'sky142',
      machineId: 'm2',
      state: 'preflighting',
      updatedAt: '',
      light1: 'yellow',
      light2: 'off',
      phase: 'preflight-net',
      installerPreflight: {
        carrierOk: false,
        clockSkewSec: 0.4,
        passed: false,
        reportedAt: '2026-07-29T00:01:05Z',
        matrix: [
          { target: 'bond0 member IF.6 (eth5) link', ok: false, detail: 'car=0,op=down' },
          { target: 'peer sky143 mgmt 10.32.10.143', ok: false, detail: 'unreachable' },
        ],
      },
    },
  },
}

describe('PreflightReportModal', () => {
  it('defaults to the problematic node and shows its failing checks', () => {
    render(
      <PreflightReportModal isOpen clusterId="c1" deploy={deploy} onClose={() => {}} />,
    )
    // sky142 (has failures) is auto-selected → its detail is shown.
    expect(screen.getByText('not passed')).toBeTruthy()
    expect(screen.getAllByText(/bond0 member IF\.6/).length).toBeGreaterThan(0)
    expect(screen.getAllByText(/peer sky143 mgmt 10\.32\.10\.143/).length).toBeGreaterThan(0)
    // both node tabs are present
    expect(screen.getByRole('button', { name: /sky141/ })).toBeTruthy()
    expect(screen.getByRole('button', { name: /sky142/ })).toBeTruthy()
  })

  it('switches to a node when its tab is clicked', async () => {
    const user = userEvent.setup()
    render(
      <PreflightReportModal isOpen clusterId="c1" deploy={deploy} onClose={() => {}} />,
    )
    await user.click(screen.getByRole('button', { name: /sky141/ }))
    expect(screen.getByText('passed')).toBeTruthy()
    expect(screen.getAllByText(/bond0 member IF\.5/).length).toBeGreaterThan(0)
  })

  it('re-run preflight POSTs the rekick endpoint', async () => {
    const user = userEvent.setup()
    fetchMock.mockResolvedValue(new Response('{"message":"ok"}', { status: 200 }))
    render(
      <PreflightReportModal isOpen clusterId="c1" deploy={deploy} onClose={() => {}} />,
    )
    // sky142 (problematic) is auto-selected; its section shows the re-run button.
    await user.click(screen.getByRole('button', { name: 'Re-run preflight' }))
    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        (c) =>
          typeof c[0] === 'string' &&
          (c[0] as string).endsWith('/deploy/preflight/rekick/sky142'),
      )
      expect(posted).toBeTruthy()
    })
  })
})
