// @vitest-environment jsdom
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { EnterprisePage } from './EnterprisePage'

describe('EnterprisePage', () => {
  it('lists both module cards with Install buttons', () => {
    render(
      <MemoryRouter>
        <EnterprisePage />
      </MemoryRouter>,
    )
    expect(screen.getByText('App-Framework')).toBeTruthy()
    // The CubeCMP and Advisor cards show a logo image (alt text) in place of
    // a heading.
    expect(screen.getByAltText('CubeCMP')).toBeTruthy()
    expect(screen.getByAltText('Cube AI Advisor')).toBeTruthy()
    expect(screen.getAllByRole('button', { name: 'Install' })).toHaveLength(3)
  })

  it('opens the real install modal for the clicked module, then closes it', async () => {
    // No fetch stub here on purpose: the modal's cluster/artifact effects
    // must tolerate a failing fetch without throwing during render.
    const user = userEvent.setup()
    render(
      <MemoryRouter>
        <EnterprisePage />
      </MemoryRouter>,
    )

    const [appfwInstall] = screen.getAllByRole('button', { name: 'Install' })
    await user.click(appfwInstall)

    expect(screen.getByText('Install App-Framework')).toBeTruthy()

    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByText('Install App-Framework')).toBeNull()
  })
})
