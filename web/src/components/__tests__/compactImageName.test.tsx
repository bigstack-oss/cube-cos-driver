import { describe, expect, it } from 'vitest'
import { compactImageName } from '../AppSidebar'
import { stripBootMode } from '../../api/deploy'

describe('image name display helpers', () => {
  it('strips the boot-mode suffix', () => {
    expect(stripBootMode('travis_cubecos (UEFI)')).toBe('travis_cubecos')
    expect(stripBootMode('img (BIOS)')).toBe('img')
    expect(stripBootMode('plain')).toBe('plain')
  })

  it('leaves short names whole, middle-truncates long ones keeping version + commit', () => {
    expect(compactImageName('travis_cubecos (UEFI)')).toBe('travis_cubecos')
    const long = compactImageName('CUBE_3.1.10_20260729-1922_ca78cdc (UEFI)')
    expect(long).toBe('CUBE_3.1.10…_ca78cdc')
    expect(long).toContain('…')
    expect(long).toMatch(/^CUBE_3\.1\.10/) // version kept
    expect(long).toMatch(/ca78cdc$/) // commit kept
  })
})
