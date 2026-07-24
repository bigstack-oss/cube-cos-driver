// @vitest-environment jsdom
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { Machine } from '../../model/machine'
import { HardwarePage } from '../hardware/HardwarePage'

const fetchMock = vi.fn()

const machine = (over: Partial<Machine> = {}): Machine => ({
  id: 'm1',
  label: 'node-1',
  bmc: { address: '10.0.0.1', username: 'admin' },
  hasPassword: true,
  fetchState: 'ok',
  inventory: {
    fetchedAt: '2026-07-24T00:00:00Z',
    source: 'redfish',
    serial: 'SN123',
    cpuModel: 'Xeon',
    cpuCount: 2,
    memoryBytes: 128 * 1024 ** 3,
    nics: [{ name: 'eth0', mac: 'aa:bb', speedMbps: 10000, up: true }],
    disks: [{ name: 'sda', type: 'SSD', sizeBytes: 480 * 1024 ** 3 }],
  },
  ...over,
})

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

describe('HardwarePage', () => {
  it('lists machines with fetched hardware facts', async () => {
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify([machine()]), { status: 200 }),
    )
    render(
      <MemoryRouter>
        <HardwarePage />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByText('node-1')).toBeTruthy())
    expect(screen.getByText('SN123')).toBeTruthy()
    expect(screen.getByText('Xeon ×2')).toBeTruthy()
    expect(screen.getByText('128 GiB')).toBeTruthy()
  })

  it('creates a machine via the modal (password sent on create)', async () => {
    const user = userEvent.setup()
    fetchMock.mockImplementation(async (_url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        return new Response(JSON.stringify(machine()), { status: 201 })
      }
      return new Response('[]', { status: 200 })
    })
    render(
      <MemoryRouter>
        <HardwarePage />
      </MemoryRouter>,
    )
    await waitFor(() => screen.getByText('Add machine'))
    await user.click(screen.getByRole('button', { name: 'Add machine' }))

    await user.type(screen.getByLabelText('Label'), 'node-x')
    await user.type(screen.getByLabelText('BMC address'), '10.0.0.9')
    await user.type(screen.getByLabelText('BMC username'), 'root')
    await user.type(screen.getByLabelText('BMC password'), 'pw')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => {
      const post = fetchMock.mock.calls.find(
        (c) => (c[1] as RequestInit | undefined)?.method === 'POST',
      )
      expect(post).toBeTruthy()
      const body = JSON.parse((post![1] as RequestInit).body as string)
      expect(body).toEqual({
        label: 'node-x',
        bmc: { address: '10.0.0.9', username: 'root', password: 'pw' },
      })
    })
  })

  it('triggers a hardware fetch', async () => {
    const user = userEvent.setup()
    fetchMock.mockImplementation(async (url: string) => {
      if (typeof url === 'string' && url.endsWith('/fetch')) {
        return new Response(JSON.stringify({ message: 'fetch started' }), {
          status: 202,
        })
      }
      return new Response(JSON.stringify([machine({ fetchState: 'idle' })]), {
        status: 200,
      })
    })
    render(
      <MemoryRouter>
        <HardwarePage />
      </MemoryRouter>,
    )
    await waitFor(() => screen.getByText('node-1'))
    await user.click(screen.getByRole('button', { name: 'Fetch' }))
    await waitFor(() => {
      const called = fetchMock.mock.calls.some(
        (c) => typeof c[0] === 'string' && c[0].endsWith('/fetch'),
      )
      expect(called).toBe(true)
    })
  })
})
