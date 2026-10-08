// @vitest-environment jsdom
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { SupportMatrix } from './SupportMatrix'

describe('SupportMatrix', () => {
  it('renders one row per release with each module version and status', () => {
    render(
      <SupportMatrix
        manifests={[
          {
            name: 'v3.2.0',
            match: { version: '3.2.0' },
            schema: 2,
            modules: {
              cmp: [{ version: '2.1.1', status: 'supported' }],
              advisor: [{ version: '0.4.25', status: 'untested' }],
            },
          },
          { name: 'v3.1.0', match: { version: '3.1.0' } },
        ]}
      />,
    )
    expect(screen.getByText('3.2.0')).toBeTruthy()
    expect(screen.getByText('2.1.1')).toBeTruthy()
    expect(screen.getByText('untested')).toBeTruthy()
    expect(screen.getByText('3.1.0')).toBeTruthy()
    expect(screen.getAllByText('not constrained').length).toBeGreaterThan(0)
  })

  it('shows a blocked entry reason in the chip title', () => {
    render(
      <SupportMatrix
        manifests={[
          {
            name: 'v3.2.0',
            match: { version: '3.2.0' },
            modules: { cmp: [{ version: '1.0.0', status: 'blocked', reason: 'CVE-1234 breaks login' }] },
          },
        ]}
      />,
    )
    expect(screen.getByText('blocked').closest('[title]')?.getAttribute('title')).toContain('CVE-1234 breaks login')
  })
})
