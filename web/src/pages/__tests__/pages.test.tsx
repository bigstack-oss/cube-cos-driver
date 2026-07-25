// @vitest-environment jsdom
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { shortId } from '../../model/types'
import { writeClusterDraft } from '../../state/clusters'
import { ha3 } from '../../testing/fixtures'
import { ClusterPage } from '../cluster/ClusterPage'
import { LandingPage } from '../landing/LandingPage'

const fetchMock = vi.fn()

const renderClusterPage = (id: string) =>
  render(
    <MemoryRouter initialEntries={[`/clusters/${id}`]}>
      <Routes>
        <Route path="/clusters/:id" element={<ClusterPage />} />
        <Route path="/" element={<div>landing</div>} />
      </Routes>
    </MemoryRouter>,
  )

beforeEach(() => {
  localStorage.clear()
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

describe('LandingPage', () => {
  it('lists local drafts and server-only clusters', async () => {
    writeClusterDraft(ha3)
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify([
          { id: 'aabbccddee01', name: 'sky-lab', nodes: [] },
          { id: 'ffffffffffff', name: 'server-one', nodes: ['n1'] },
        ]),
        { status: 200 },
      ),
    )
    render(
      <MemoryRouter>
        <LandingPage />
      </MemoryRouter>,
    )
    expect(screen.getByText('sky-lab')).toBeTruthy()
    await waitFor(() => expect(screen.getByText('server-one')).toBeTruthy())
    expect(screen.getByText('server')).toBeTruthy()
    // Role breakdown from the local draft.
    expect(screen.getByText('control-converged × 3')).toBeTruthy()
  })
})

describe('ClusterPage', () => {
  it('renders draft, saves via PUT, then offers downloads', async () => {
    const user = userEvent.setup()
    writeClusterDraft(ha3)
    fetchMock.mockImplementation(async (_url: string, init?: RequestInit) => {
      if (init?.method === 'PUT') {
        return new Response(JSON.stringify({ message: 'saved' }), {
          status: 200,
        })
      }
      return new Response(JSON.stringify({ message: 'nope' }), {
        status: 404,
      })
    })

    renderClusterPage(shortId(ha3.clusterInfo.id))

    expect(screen.getByText('sky-lab')).toBeTruthy()
    // Hostname renders in both the node table and the cluster diagram.
    expect(screen.getAllByText('cube-1').length).toBeGreaterThan(0)

    const save = screen.getByRole('button', { name: 'Save to server' })
    expect(save.hasAttribute('disabled')).toBe(false)
    await user.click(save)

    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Saved' })).toBeTruthy(),
    )
    const putCall = fetchMock.mock.calls.find(
      (c) => (c[1] as RequestInit | undefined)?.method === 'PUT',
    )!
    expect(putCall[0]).toBe('/api/v1/clusters/aabbccddee01')
    const body = JSON.parse((putCall[1] as RequestInit).body as string)
    expect(body.nodeData).toHaveLength(3)

    expect(
      screen.getByRole('button', { name: 'Download cluster zip' }),
    ).toBeTruthy()
    expect(
      screen.getByRole('button', { name: 'Get snapshot URLs' }),
    ).toBeTruthy()
  })

  it('disables save when validation fails', () => {
    const broken = JSON.parse(JSON.stringify(ha3)) as typeof ha3
    broken.nodeData[1].hostname = broken.nodeData[0].hostname
    writeClusterDraft(broken)
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ message: 'nope' }), { status: 404 }),
    )

    renderClusterPage(shortId(broken.clusterInfo.id))

    expect(screen.getByText('Hostname error')).toBeTruthy()
    expect(
      screen
        .getByRole('button', { name: 'Save to server' })
        .hasAttribute('disabled'),
    ).toBe(true)
  })
})
