import { describe, expect, it } from 'vitest'
import type { DayUsage } from '../api/types'
import { byCustomer, csvHeader, emptyTotals, formatPercent, safeCell, shareOfWork, summarize, usageCsv, usageRange, utcDay } from './usage'

const row = (over: Partial<DayUsage>): DayUsage => ({
  day: '2026-10-20',
  customer_id: 'c1',
  device_id: 'd1',
  windows: 0,
  allocated_seconds: 0,
  operator_accepted: 0,
  operator_rejected: 0,
  operator_difficulty: 0,
  customer_accepted: 0,
  customer_rejected: 0,
  customer_difficulty: 0,
  ...over,
})

const rows: DayUsage[] = [
  row({ day: '2026-10-19', device_id: 'd1', rule_id: 'global', windows: 3, allocated_seconds: 2700, operator_accepted: 30, operator_difficulty: 300, customer_accepted: 270, customer_difficulty: 2700 }),
  row({ day: '2026-10-20', device_id: 'd1', rule_id: 'global', windows: 2, allocated_seconds: 1800, operator_accepted: 20, operator_rejected: 1, operator_difficulty: 200, customer_accepted: 180, customer_rejected: 2, customer_difficulty: 1800 }),
  row({ day: '2026-10-20', device_id: 'd2', rule_id: 'global', windows: 1, allocated_seconds: 900, operator_accepted: 10, operator_difficulty: 100, customer_accepted: 90, customer_difficulty: 900 }),
  row({ customer_id: 'c2', device_id: 'd9', windows: 6, allocated_seconds: 5400, operator_accepted: 60, operator_difficulty: 600, customer_accepted: 540, customer_difficulty: 5400 }),
]

describe('usageRange', () => {
  const now = new Date('2026-10-20T23:30:00Z')

  it('counts today as the last day of the range, in UTC', () => {
    expect(usageRange('7d', now)).toEqual({ from: '2026-10-14', to: '2026-10-20' })
    expect(usageRange('30d', now)).toEqual({ from: '2026-09-21', to: '2026-10-20' })
    expect(usageRange('90d', now)).toEqual({ from: '2026-07-23', to: '2026-10-20' })
  })

  it('uses the UTC day even when the local day is another one', () => {
    expect(utcDay(new Date('2026-10-20T23:59:59Z'))).toBe('2026-10-20')
    expect(utcDay(new Date('2026-10-21T00:00:00Z'))).toBe('2026-10-21')
  })
})

describe('summarize', () => {
  it('adds up every column', () => {
    const t = summarize(rows)
    expect(t.windows).toBe(12)
    expect(t.allocatedSeconds).toBe(10800)
    expect(t.operatorAccepted).toBe(120)
    expect(t.operatorRejected).toBe(1)
    expect(t.customerAccepted).toBe(1080)
    expect(t.customerRejected).toBe(2)
    expect(t.operatorDifficulty).toBe(1200)
    expect(t.customerDifficulty).toBe(10800)
  })

  it('is all zeros without rows', () => {
    expect(summarize([])).toEqual(emptyTotals())
  })
})

describe('shareOfWork', () => {
  it('is the operator part of the accepted work', () => {
    expect(shareOfWork(summarize(rows))).toBeCloseTo(0.1, 10)
  })

  it('is zero, not NaN, when there is no work', () => {
    expect(shareOfWork(emptyTotals())).toBe(0)
  })
})

describe('byCustomer', () => {
  const list = byCustomer(rows)

  it('gives one line per customer', () => {
    expect(list).toHaveLength(2)
  })

  it('puts the longest operator time first', () => {
    const more = byCustomer([...rows, row({ day: '2026-10-18', customer_id: 'c2', device_id: 'd9', allocated_seconds: 60 })])
    expect(more.map((c) => [c.customerId, c.allocatedSeconds])).toEqual([
      ['c2', 5460],
      ['c1', 5400],
    ])
  })

  it('orders customers with the same time by id, so that the list does not change between reads', () => {
    // c1 and c2 both have 5400 seconds in the sample rows.
    expect(list.map((c) => [c.customerId, c.allocatedSeconds])).toEqual([
      ['c1', 5400],
      ['c2', 5400],
    ])
  })

  it('counts the devices and days with activity, and the rules', () => {
    const c1 = list.find((c) => c.customerId === 'c1')
    expect(c1?.devices).toBe(2)
    expect(c1?.days).toBe(2)
    expect(c1?.rules).toEqual(['global'])
    expect(c1?.allocatedSeconds).toBe(5400)
  })

  it('does not mix customers', () => {
    expect(list.find((c) => c.customerId === 'c2')?.operatorAccepted).toBe(60)
  })

  it('is empty without rows', () => {
    expect(byCustomer([])).toEqual([])
  })
})

describe('safeCell', () => {
  it('neutralizes what a spreadsheet would run as a formula', () => {
    for (const bad of ['=1+1', '+1', '-1', '@sum', '\tx', '\rx']) expect(safeCell(bad), bad).toBe(`'${bad}`)
  })

  it('leaves ordinary text alone', () => {
    expect(safeCell('Acme Mining')).toBe('Acme Mining')
    expect(safeCell('')).toBe('')
    expect(safeCell('شرکت')).toBe('شرکت')
  })
})

describe('usageCsv', () => {
  const names = { customer: (id: string) => (id === 'c1' ? 'Acme, "Mining"' : '=HYPERLINK("x")'), device: (id: string) => `acme.${id}` }
  const csv = usageCsv(rows, names)
  const lines = csv.replace('﻿', '').trimEnd().split('\n')

  it('starts with a byte order mark, so that Excel reads Persian and Russian', () => {
    expect(csv.startsWith('﻿')).toBe(true)
  })

  it('has the header and one line per row, ordered by day, customer and device', () => {
    expect(lines[0]).toBe(csvHeader.join(','))
    expect(lines).toHaveLength(1 + rows.length)
    expect(lines.slice(1).map((l) => l.split(',')[0])).toEqual(['2026-10-19', '2026-10-20', '2026-10-20', '2026-10-20'])
  })

  it('quotes a name with a comma or a quote, and neutralizes a formula', () => {
    expect(csv).toContain('"Acme, ""Mining"""')
    expect(csv).toContain(`"'=HYPERLINK(""x"")"`)
  })

  it('writes numbers as numbers and the share of work as a percent', () => {
    expect(lines[1]).toBe('2026-10-19,c1,"Acme, ""Mining""",d1,acme.d1,global,3,2700,30,0,300,270,0,2700,10.0000')
  })

  it('says 0 for a row without work', () => {
    const empty = usageCsv([row({})], names).trimEnd().split('\n')[1]
    expect(empty?.endsWith(',0,0,0,0,0,0,0,0,0.0000')).toBe(true)
  })
})

describe('formatPercent', () => {
  it('shows a fraction as a percent with at most two decimals', () => {
    expect(formatPercent(0.1, 'en')).toBe('10%')
    expect(formatPercent(1 / 24, 'en')).toBe('4.17%')
    expect(formatPercent(0, 'en')).toBe('0%')
    expect(formatPercent(0.00004, 'en')).toBe('0%')
  })
})
