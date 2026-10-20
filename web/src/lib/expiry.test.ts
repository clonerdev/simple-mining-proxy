import { describe, expect, it } from 'vitest'
import { addMonths, expiryState, fromLocalInput, presetEnd, resolveExpiry, toApiTime, toLocalInput } from './expiry'

// Dates are built from local parts, so the results do not depend on the
// time zone of the machine that runs the tests.
const at = (y: number, m: number, d: number, h = 12, min = 0) => new Date(y, m - 1, d, h, min)
const parts = (d: Date) => [d.getFullYear(), d.getMonth() + 1, d.getDate(), d.getHours(), d.getMinutes()]

describe('addMonths', () => {
  it('keeps the day and the time of day', () => {
    expect(parts(addMonths(at(2026, 3, 15, 9, 30), 1))).toEqual([2026, 4, 15, 9, 30])
  })

  it('ends on the last day of a shorter month, never in the next one', () => {
    expect(parts(addMonths(at(2026, 1, 31), 1))).toEqual([2026, 2, 28, 12, 0])
    expect(parts(addMonths(at(2028, 1, 31), 1))).toEqual([2028, 2, 29, 12, 0])
    expect(parts(addMonths(at(2026, 8, 31), 1))).toEqual([2026, 9, 30, 12, 0])
  })

  it('crosses the end of the year', () => {
    expect(parts(addMonths(at(2026, 12, 31), 2))).toEqual([2027, 2, 28, 12, 0])
    expect(parts(addMonths(at(2026, 11, 30), 3))).toEqual([2027, 2, 28, 12, 0])
  })

  it('does not change the date it is given', () => {
    const from = at(2026, 1, 31)
    addMonths(from, 1)
    expect(parts(from)).toEqual([2026, 1, 31, 12, 0])
  })
})

describe('presetEnd', () => {
  const from = at(2026, 10, 20, 14, 0)
  it('counts a week, months and a year', () => {
    expect(parts(presetEnd('1w', from)!)).toEqual([2026, 10, 27, 14, 0])
    expect(parts(presetEnd('1m', from)!)).toEqual([2026, 11, 20, 14, 0])
    expect(parts(presetEnd('3m', from)!)).toEqual([2027, 1, 20, 14, 0])
    expect(parts(presetEnd('6m', from)!)).toEqual([2027, 4, 20, 14, 0])
    expect(parts(presetEnd('1y', from)!)).toEqual([2027, 10, 20, 14, 0])
  })
  it('has no end for "none"', () => {
    expect(presetEnd('none', from)).toBeNull()
  })
})

describe('time formats', () => {
  it('sends whole seconds in UTC', () => {
    expect(toApiTime(new Date(Date.UTC(2026, 10, 20, 8, 30, 15, 789)))).toBe('2026-11-20T08:30:15Z')
  })

  it('round-trips a datetime-local value', () => {
    const d = at(2026, 3, 5, 7, 4)
    expect(toLocalInput(d)).toBe('2026-03-05T07:04')
    expect(parts(fromLocalInput('2026-03-05T07:04')!)).toEqual([2026, 3, 5, 7, 4])
  })

  it('rejects what is not a date', () => {
    for (const v of ['', 'tomorrow', '2026-03-05', '2026-13-45T10:00']) expect(fromLocalInput(v), v).toBeNull()
  })
})

describe('resolveExpiry', () => {
  const now = at(2026, 10, 20, 14, 0)

  it('turns a preset into a time counted from the base', () => {
    const r = resolveExpiry('1m', '', now, now)
    expect(r).toEqual({ kind: 'set', iso: toApiTime(at(2026, 11, 20, 14, 0)) })
  })

  it('counts from a later base, for extending a device that has not ended', () => {
    const end = at(2026, 12, 1, 8, 0)
    expect(resolveExpiry('1m', '', end, now)).toEqual({ kind: 'set', iso: toApiTime(at(2027, 1, 1, 8, 0)) })
  })

  it('means no end for "none", and leaves it alone for "keep"', () => {
    expect(resolveExpiry('none', '', now, now)).toEqual({ kind: 'set', iso: null })
    expect(resolveExpiry('keep', '', now, now)).toEqual({ kind: 'keep' })
  })

  it('takes a typed time only if it is a date in the future', () => {
    expect(resolveExpiry('custom', '2026-12-25T10:00', now, now)).toEqual({ kind: 'set', iso: toApiTime(at(2026, 12, 25, 10, 0)) })
    expect(resolveExpiry('custom', '', now, now)).toEqual({ kind: 'error', error: 'invalid' })
    expect(resolveExpiry('custom', 'soon', now, now)).toEqual({ kind: 'error', error: 'invalid' })
    expect(resolveExpiry('custom', '2026-10-20T13:59', now, now)).toEqual({ kind: 'error', error: 'past' })
    expect(resolveExpiry('custom', '2026-10-20T14:00', now, now)).toEqual({ kind: 'error', error: 'past' })
  })
})

describe('expiryState', () => {
  const now = Date.UTC(2026, 9, 20, 12, 0, 0)
  const iso = (ms: number) => new Date(now + ms).toISOString()
  const day = 24 * 3600 * 1000

  it('has no state without an end', () => {
    expect(expiryState(null, now)).toEqual({ kind: 'none' })
    expect(expiryState(undefined, now)).toEqual({ kind: 'none' })
  })

  it('tells a long way off from soon and from over', () => {
    expect(expiryState(iso(30 * day), now)).toEqual({ kind: 'active', ms: 30 * day })
    expect(expiryState(iso(7 * day), now)).toEqual({ kind: 'soon', ms: 7 * day })
    expect(expiryState(iso(2 * day), now)).toEqual({ kind: 'soon', ms: 2 * day })
    expect(expiryState(iso(-3 * day), now)).toEqual({ kind: 'expired', ms: 3 * day })
    expect(expiryState(iso(0), now)).toEqual({ kind: 'expired', ms: 0 })
  })
})
