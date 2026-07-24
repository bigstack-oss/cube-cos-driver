// Timezone options from the Intl API (replaces the legacy bundled
// timezones.json + moment-timezone).
import { Timezone } from '../../../model/types'
import { SelectOption } from '../../form/Select'

const offsetMinutes = (tz: string): number => {
  try {
    const now = new Date()
    const utc = new Date(now.toLocaleString('en-US', { timeZone: 'UTC' }))
    const local = new Date(now.toLocaleString('en-US', { timeZone: tz }))
    return Math.round((local.getTime() - utc.getTime()) / 60000)
  } catch {
    return 0
  }
}

const formatOffset = (minutes: number): string => {
  const sign = minutes < 0 ? '-' : '+'
  const abs = Math.abs(minutes)
  const h = String(Math.floor(abs / 60)).padStart(2, '0')
  const m = String(abs % 60).padStart(2, '0')
  return `UTC${sign}${h}:${m}`
}

let cached: SelectOption<Timezone>[] | null = null

export const getTimezoneOptions = (): SelectOption<Timezone>[] => {
  if (cached) return cached
  const intl = Intl as typeof Intl & {
    supportedValuesOf?: (key: 'timeZone') => string[]
  }
  const names: string[] =
    typeof intl.supportedValuesOf === 'function'
      ? intl.supportedValuesOf('timeZone')
      : ['UTC', 'Asia/Taipei']
  cached = names.map((name) => {
    const offset = offsetMinutes(name)
    return {
      value: { name, offset },
      label: `${name} (${formatOffset(offset)})`,
    }
  })
  return cached
}

export const guessTimezone = (): Timezone => {
  const name = Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Taipei'
  return { name, offset: offsetMinutes(name) }
}
