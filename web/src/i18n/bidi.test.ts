import { describe, expect, it } from 'vitest'
import { isolateTechnicalRuns } from './bidi'

const LRI = '⁦'
const PDI = '⁩'
const wrap = (s: string) => LRI + s + PDI

describe('isolateTechnicalRuns', () => {
  it('keeps a number and its unit together', () => {
    expect(isolateTechnicalRuns('مجموع ≈ 48.38 TH/s')).toBe(`مجموع ≈ ${wrap('48.38 TH/s')}`)
  })

  it('keeps a leading dot or slash with its word', () => {
    expect(isolateTechnicalRuns('فایل .env')).toBe(`فایل ${wrap('.env')}`)
    expect(isolateTechnicalRuns('دستور /status')).toBe(`دستور ${wrap('/status')}`)
  })

  it('keeps URL schemes whole', () => {
    expect(isolateTechnicalRuns('آدرس stratum+tls:// را')).toBe(`آدرس ${wrap('stratum+tls://')} را`)
  })

  it('leaves the full stop of the sentence outside', () => {
    expect(isolateTechnicalRuns('رمز عبور: x.')).toBe(`رمز عبور: ${wrap('x')}.`)
  })

  it('wraps a date and a time as one value', () => {
    expect(isolateTechnicalRuns('در 10/15 22:16:11 رخ داد')).toBe(`در ${wrap('10/15 22:16:11')} رخ داد`)
  })

  it('keeps a percentage together', () => {
    expect(isolateTechnicalRuns('ردشده 8 (5.48%)')).toBe(`ردشده 8 (${wrap('5.48%')})`)
  })

  it('is idempotent, so a value translated on its own can be nested', () => {
    const once = isolateTechnicalRuns('آدرس stratum+ssl://<HOST>:14443 را')
    expect(isolateTechnicalRuns(once)).toBe(once)
    const nested = `آدرس stratum+ssl://<${wrap('HOST>')}:14443 را`
    expect(isolateTechnicalRuns(nested)).toBe(once)
  })

  it('does not touch plain numbers or Persian text', () => {
    expect(isolateTechnicalRuns('هر 30 دقیقه')).toBe('هر 30 دقیقه')
  })
})
