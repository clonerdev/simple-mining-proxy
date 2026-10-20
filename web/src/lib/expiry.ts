// The end time of a device or a customer: presets such as "one month", a
// date typed by hand, or no end at all. Pure functions, so the month
// arithmetic is tested without a browser.

export const presets = ['1w', '1m', '3m', '6m', '1y', 'none', 'custom'] as const
/** "keep" leaves an end that is already set as it is; it is offered only when editing. */
export type Preset = (typeof presets)[number] | 'keep'

/**
 * from plus whole months, keeping the day of the month and the time of day.
 * A day the target month does not have becomes that month's last day, so one
 * month after 31 January is 28 (or 29) February, never 3 March.
 */
export function addMonths(from: Date, months: number): Date {
  const d = new Date(from.getTime())
  const day = d.getDate()
  d.setDate(1)
  d.setMonth(d.getMonth() + months)
  const lastDay = new Date(d.getFullYear(), d.getMonth() + 1, 0).getDate()
  d.setDate(Math.min(day, lastDay))
  return d
}

/** The end a preset gives when counted from `from`; null for "no end". */
export function presetEnd(preset: Exclude<Preset, 'custom' | 'keep'>, from: Date): Date | null {
  switch (preset) {
    case '1w': {
      const d = new Date(from.getTime())
      d.setDate(d.getDate() + 7)
      return d
    }
    case '1m':
      return addMonths(from, 1)
    case '3m':
      return addMonths(from, 3)
    case '6m':
      return addMonths(from, 6)
    case '1y':
      return addMonths(from, 12)
    case 'none':
      return null
  }
}

/** A time as the API wants it: RFC 3339, UTC, whole seconds. */
export function toApiTime(d: Date): string {
  return d.toISOString().replace(/\.\d{3}Z$/, 'Z')
}

const two = (n: number) => String(n).padStart(2, '0')

/** The value of an <input type="datetime-local"> for a time, in local time. */
export function toLocalInput(d: Date): string {
  return `${d.getFullYear()}-${two(d.getMonth() + 1)}-${two(d.getDate())}T${two(d.getHours())}:${two(d.getMinutes())}`
}

/** The time a datetime-local value means, in local time; null when it is empty or not a date. */
export function fromLocalInput(value: string): Date | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) return null
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? null : d
}

export type Resolved =
  /** Send this end (null clears it). */
  | { kind: 'set'; iso: string | null }
  /** Leave the end as it is. */
  | { kind: 'keep' }
  | { kind: 'error'; error: 'invalid' | 'past' }

/** What a choice means, counting presets from `from` and judging "past" against `now`. */
export function resolveExpiry(preset: Preset, custom: string, from: Date, now: Date): Resolved {
  if (preset === 'keep') return { kind: 'keep' }
  if (preset === 'custom') {
    const d = fromLocalInput(custom)
    if (!d) return { kind: 'error', error: 'invalid' }
    if (d.getTime() <= now.getTime()) return { kind: 'error', error: 'past' }
    return { kind: 'set', iso: toApiTime(d) }
  }
  const end = presetEnd(preset, from)
  if (!end) return { kind: 'set', iso: null }
  if (end.getTime() <= now.getTime()) return { kind: 'error', error: 'past' }
  return { kind: 'set', iso: toApiTime(end) }
}

const week = 7 * 24 * 3600 * 1000

export type ExpiryState =
  | { kind: 'none' }
  | { kind: 'active'; ms: number }
  /** Ends within a week. */
  | { kind: 'soon'; ms: number }
  | { kind: 'expired'; ms: number }

/** Where an end stands at `now`; ms is the time left, or since it passed. */
export function expiryState(iso: string | null | undefined, now: number): ExpiryState {
  if (!iso) return { kind: 'none' }
  const left = new Date(iso).getTime() - now
  if (left <= 0) return { kind: 'expired', ms: Math.abs(left) }
  return { kind: left <= week ? 'soon' : 'active', ms: left }
}
