// @vitest-environment jsdom
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { App } from '../App'

describe('App shell', () => {
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

  it('routes to the cluster page', () => {
    render(
      <MemoryRouter initialEntries={['/clusters/deadbeef0001']}>
        <App />
      </MemoryRouter>,
    )
    expect(screen.getByText(/Cluster deadbeef0001/)).toBeTruthy()
  })
})
