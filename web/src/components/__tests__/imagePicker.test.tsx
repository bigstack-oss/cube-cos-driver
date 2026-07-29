// @vitest-environment jsdom
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ImagePicker } from '../ImagePicker'

const fetchMock = vi.fn()
beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

const imagesResp = (imgs: unknown[]) =>
  new Response(JSON.stringify({ images: imgs }), { status: 200 })

describe('ImagePicker', () => {
  it('renders nothing when fewer than 2 images', async () => {
    fetchMock.mockResolvedValue(imagesResp([{ name: 'only', default: true }]))
    const { container } = render(<ImagePicker onChange={() => {}} />)
    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    expect(container.querySelector('select')).toBeNull()
  })

  it('lists images; non-default pick reports its name, default reports empty', async () => {
    fetchMock.mockResolvedValue(
      imagesResp([
        { name: 'travis_cubecos (UEFI)', default: true },
        { name: 'v3.1.0-rc6 (UEFI)', default: false },
      ]),
    )
    const onChange = vi.fn()
    const user = userEvent.setup()
    render(<ImagePicker onChange={onChange} />)
    await waitFor(() => expect(screen.getByRole('combobox')).toBeTruthy())
    await user.selectOptions(screen.getByRole('combobox'), 'v3.1.0-rc6 (UEFI)')
    expect(onChange).toHaveBeenLastCalledWith('v3.1.0-rc6 (UEFI)')
    await user.selectOptions(screen.getByRole('combobox'), 'travis_cubecos (UEFI)')
    expect(onChange).toHaveBeenLastCalledWith('')
  })
})
