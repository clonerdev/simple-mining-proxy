import { describe, expect, it } from 'vitest'
import { amountText, effectiveLevel, parseAmount, shareFraction, validShare } from './share'

describe('shareFraction', () => {
  it('turns a percent and minutes per day into a fraction of the time', () => {
    expect(shareFraction({ kind: 'percent', amount: 5 })).toBeCloseTo(0.05, 10)
    expect(shareFraction({ kind: 'minutes_per_day', amount: 60 })).toBeCloseTo(1 / 24, 10)
    expect(shareFraction({ kind: 'percent', amount: 0 })).toBe(0)
  })

  it('is zero for what the server would refuse', () => {
    expect(shareFraction({ kind: 'percent', amount: 101 })).toBe(0)
    expect(shareFraction({ kind: 'minutes_per_day', amount: 1441 })).toBe(0)
    expect(shareFraction({ kind: 'percent', amount: -1 })).toBe(0)
    expect(shareFraction({ kind: 'percent', amount: Number.NaN })).toBe(0)
  })
})

describe('validShare', () => {
  it('accepts zero and the whole, nothing outside', () => {
    expect(validShare('percent', 0)).toBe(true)
    expect(validShare('percent', 100)).toBe(true)
    expect(validShare('minutes_per_day', 1440)).toBe(true)
    expect(validShare('percent', 100.01)).toBe(false)
    expect(validShare('minutes_per_day', -1)).toBe(false)
  })
})

describe('parseAmount', () => {
  it('reads plain numbers, with a point or a comma', () => {
    expect(parseAmount('5')).toBe(5)
    expect(parseAmount(' 2.5 ')).toBe(2.5)
    expect(parseAmount('2,5')).toBe(2.5)
    expect(parseAmount('0')).toBe(0)
  })

  it('refuses everything else', () => {
    for (const bad of ['', ' ', 'abc', '-1', '1e3', '1.234', '1,2,3', '12345', '5%']) expect(parseAmount(bad), bad).toBeNull()
  })
})

describe('amountText', () => {
  it('drops trailing zeros', () => {
    expect(amountText(5)).toBe('5')
    expect(amountText(2.5)).toBe('2.5')
    expect(amountText(2.05)).toBe('2.05')
  })
})

describe('effectiveLevel', () => {
  const rule = { kind: 'percent' as const, amount: 1 }
  it('takes the device first, then the customer, then the setting', () => {
    expect(effectiveLevel({ operator_share: rule }, { operator_share: rule })).toBe('device')
    expect(effectiveLevel({}, { operator_share: rule })).toBe('customer')
    expect(effectiveLevel({}, {})).toBe('setting')
    expect(effectiveLevel({}, undefined)).toBe('setting')
  })

  it('a device left out (amount 0) still counts as its own rule', () => {
    expect(effectiveLevel({ operator_share: { kind: 'percent', amount: 0 } }, { operator_share: rule })).toBe('device')
  })
})
