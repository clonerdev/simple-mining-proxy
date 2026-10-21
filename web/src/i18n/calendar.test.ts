import { describe, expect, it } from 'vitest'
import { localeFor } from './calendar'

describe('localeFor', () => {
  it('uses the Jalali calendar with Latin digits only for Persian', () => {
    expect(localeFor('fa', 'persian')).toBe('fa-IR-u-ca-persian-nu-latn')
    expect(localeFor('en', 'persian')).toBe('en-GB')
    expect(localeFor('ru', 'persian')).toBe('ru-RU')
  })

  it('keeps the Gregorian calendar and Latin digits by default', () => {
    expect(localeFor('fa', 'gregory')).toBe('fa-IR-u-ca-gregory-nu-latn')
  })

  it('shows the same moment in the Jalali calendar when asked', () => {
    // 2026-03-21 is 1 Farvardin 1405.
    const d = new Date('2026-03-21T12:00:00Z')
    const greg = d.toLocaleDateString(localeFor('fa', 'gregory'), { year: 'numeric', month: '2-digit', day: '2-digit', timeZone: 'UTC' })
    const jal = d.toLocaleDateString(localeFor('fa', 'persian'), { year: 'numeric', month: '2-digit', day: '2-digit', timeZone: 'UTC' })
    expect(greg).toBe('2026/03/21')
    expect(jal).toBe('1405/01/01')
  })
})
