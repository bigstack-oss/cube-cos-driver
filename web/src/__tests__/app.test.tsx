// @vitest-environment jsdom
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from '../App'

describe('App shell', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('[]', { status: 200 })),
    )
  })

  it('renders the header and landing page at /', () => {
    render(
      <MemoryRouter initialEntries={['/']}>
        <App />
      </MemoryRouter>,
    )
    expect(screen.getByText('Cube Snapshot Generator')).toBeTruthy()
    expect(
      screen.getByText('Welcome to Cube Snapshot Generator'),
    ).toBeTruthy()
  })

  it('routes to the cluster page (unknown id shows not-found)', () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('{}', { status: 404 })),
    )
    render(
      <MemoryRouter initialEntries={['/clusters/deadbeef0001']}>
        <App />
      </MemoryRouter>,
    )
    expect(screen.getByText('Cluster not found')).toBeTruthy()
  })
})
