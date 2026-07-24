// @vitest-environment jsdom
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ha3 } from '../../../testing/fixtures'
import { ClusterWizard } from '../cluster/ClusterWizard'

const NEW_ID = '99999999-0000-0000-0000-000000000099'

const next = () => screen.getByRole('button', { name: 'Next' })
const finish = () => screen.getByRole('button', { name: 'Save' })

describe('ClusterWizard', () => {
  it('walks the create flow and assembles the config', async () => {
    const user = userEvent.setup()
    const onFinish = vi.fn()
    render(
      <ClusterWizard
        isOpen
        newClusterId={NEW_ID}
        onCancel={() => {}}
        onFinish={onFinish}
      />,
    )

    // License step blocks until accepted.
    expect(next().hasAttribute('disabled')).toBe(true)
    await user.click(screen.getByLabelText('I understand and accept'))
    await user.click(next())

    // Name (random default present).
    const nameInput = screen.getByLabelText('Cluster name') as HTMLInputElement
    expect(nameInput.value).toMatch(/^Cluster_/)
    await user.clear(nameInput)
    await user.type(nameInput, 'my-cluster')
    await user.click(next())

    // DNS default 8.8.8.8.
    expect(
      (screen.getByLabelText('Primary DNS') as HTMLInputElement).value,
    ).toBe('8.8.8.8')
    await user.click(next())

    // Timezone default guessed.
    await user.click(next())

    // Role settings defaults valid.
    expect(
      (screen.getByLabelText('Management CIDR') as HTMLInputElement).value,
    ).toBe('10.254.0.0/16')
    await user.click(next())

    // HA off by default → finish.
    await user.click(finish())

    expect(onFinish).toHaveBeenCalledTimes(1)
    const [info, config] = onFinish.mock.calls[0]
    expect(info).toEqual({ id: NEW_ID, name: 'my-cluster' })
    expect(config.DNS).toEqual(['8.8.8.8'])
    expect(config.HA).toBe(false)
    expect(config.HASettings).toEqual({})
    expect(config.roleSettings.secretSeed).toMatch(/^[a-z]{6}$/)
  })

  it('prefills and skips license in edit mode', () => {
    render(
      <ClusterWizard
        isOpen
        initialInfo={ha3.clusterInfo}
        initialConfig={ha3.clusterConfig}
        newClusterId={NEW_ID}
        onCancel={() => {}}
        onFinish={() => {}}
      />,
    )
    // First step is Name, prefilled.
    expect(
      (screen.getByLabelText('Cluster name') as HTMLInputElement).value,
    ).toBe('sky-lab')
    expect(screen.queryByLabelText('I understand and accept')).toBeNull()
  })

  it('blocks Next on invalid DNS', async () => {
    const user = userEvent.setup()
    render(
      <ClusterWizard
        isOpen
        initialInfo={ha3.clusterInfo}
        initialConfig={ha3.clusterConfig}
        newClusterId={NEW_ID}
        onCancel={() => {}}
        onFinish={() => {}}
      />,
    )
    await user.click(next()) // Name → DNS
    const dns = screen.getByLabelText('Primary DNS')
    await user.clear(dns)
    await user.type(dns, 'not-an-ip')
    expect(next().hasAttribute('disabled')).toBe(true)
  })
})
