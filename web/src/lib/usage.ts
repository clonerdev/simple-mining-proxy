import type { DayUsage } from '../api/types'

// The account of the operator's share: sums of the rows the server gives for a
// period, and the CSV of them. It counts work, never money: the pool pays
// whoever a share was credited to. Pure functions, tested without a browser.

export const ranges = ['7d', '30d', '90d'] as const
export type UsageRange = (typeof ranges)[number]

const length: Record<UsageRange, number> = { '7d': 7, '30d': 30, '90d': 90 }

/** A UTC day as the API wants it: 2006-01-02. */
export const utcDay = (d: Date): string => d.toISOString().slice(0, 10)

/** The days of a range, counting today (UTC) as the last one. */
export function usageRange(range: UsageRange, now: Date): { from: string; to: string } {
  const start = new Date(now.getTime() - (length[range] - 1) * 86_400_000)
  return { from: utcDay(start), to: utcDay(now) }
}

export interface Totals {
  windows: number
  allocatedSeconds: number
  operatorAccepted: number
  operatorRejected: number
  customerAccepted: number
  customerRejected: number
  operatorDifficulty: number
  customerDifficulty: number
}

export const emptyTotals = (): Totals => ({
  windows: 0,
  allocatedSeconds: 0,
  operatorAccepted: 0,
  operatorRejected: 0,
  customerAccepted: 0,
  customerRejected: 0,
  operatorDifficulty: 0,
  customerDifficulty: 0,
})

function add(t: Totals, r: DayUsage): void {
  t.windows += r.windows
  t.allocatedSeconds += r.allocated_seconds
  t.operatorAccepted += r.operator_accepted
  t.operatorRejected += r.operator_rejected
  t.customerAccepted += r.customer_accepted
  t.customerRejected += r.customer_rejected
  t.operatorDifficulty += r.operator_difficulty
  t.customerDifficulty += r.customer_difficulty
}

export function summarize(rows: DayUsage[]): Totals {
  const t = emptyTotals()
  for (const r of rows) add(t, r)
  return t
}

/** The part of the accepted work (by difficulty) that was credited to the operator, 0 to 1; 0 without any work. */
export function shareOfWork(t: Pick<Totals, 'operatorDifficulty' | 'customerDifficulty'>): number {
  const total = t.operatorDifficulty + t.customerDifficulty
  return total > 0 ? t.operatorDifficulty / total : 0
}

export interface CustomerUsage extends Totals {
  customerId: string
  /** Devices and days that had any activity in the period. */
  devices: number
  days: number
  /** The rules the share came from, without repeats. */
  rules: string[]
}

/** One line per customer, the one with the longest operator time first. */
export function byCustomer(rows: DayUsage[]): CustomerUsage[] {
  const groups = new Map<string, DayUsage[]>()
  for (const r of rows) groups.set(r.customer_id, [...(groups.get(r.customer_id) ?? []), r])
  return [...groups.entries()]
    .map(([customerId, list]) => ({
      ...summarize(list),
      customerId,
      devices: new Set(list.map((r) => r.device_id)).size,
      days: new Set(list.map((r) => r.day)).size,
      rules: [...new Set(list.map((r) => r.rule_id).filter((x): x is string => !!x))].sort(),
    }))
    .sort((a, b) => b.allocatedSeconds - a.allocatedSeconds || a.customerId.localeCompare(b.customerId))
}

/** A text that a spreadsheet would run as a formula gets a leading apostrophe (the server's export does the same). */
export function safeCell(s: string): string {
  return /^[=+\-@\t\r]/.test(s) ? `'${s}` : s
}

const quote = (s: string): string => (/[",\n\r]/.test(s) ? `"${s.replaceAll('"', '""')}"` : s)

export interface Names {
  customer: (id: string) => string
  device: (id: string) => string
}

export const csvHeader = [
  'day',
  'customer_id',
  'customer',
  'device_id',
  'device',
  'rule',
  'windows',
  'allocated_seconds',
  'operator_accepted',
  'operator_rejected',
  'operator_difficulty',
  'customer_accepted',
  'customer_rejected',
  'customer_difficulty',
  'operator_share_of_work_percent',
] as const

/**
 * The rows as CSV with a byte order mark, so that Excel reads Persian and
 * Russian names. Text cells are made safe, numbers are written as numbers.
 */
export function usageCsv(rows: DayUsage[], names: Names): string {
  const sorted = [...rows].sort(
    (a, b) => a.day.localeCompare(b.day) || a.customer_id.localeCompare(b.customer_id) || a.device_id.localeCompare(b.device_id),
  )
  const lines = [csvHeader.join(',')]
  for (const r of sorted) {
    const text = [r.day, r.customer_id, names.customer(r.customer_id), r.device_id, names.device(r.device_id), r.rule_id ?? ''].map(
      (c) => quote(safeCell(c)),
    )
    const share = (shareOfWork({ operatorDifficulty: r.operator_difficulty, customerDifficulty: r.customer_difficulty }) * 100).toFixed(4)
    const numbers = [
      r.windows,
      r.allocated_seconds,
      r.operator_accepted,
      r.operator_rejected,
      r.operator_difficulty,
      r.customer_accepted,
      r.customer_rejected,
      r.customer_difficulty,
      share,
    ].map(String)
    lines.push([...text, ...numbers].join(','))
  }
  return `﻿${lines.join('\n')}\n`
}

/** A fraction of 0 to 1 as a percent in the language of the screen: "10%", "4.17%", "0.5%". */
export function formatPercent(fraction: number, locale: string): string {
  return `${(fraction * 100).toLocaleString(locale, { maximumFractionDigits: 2 })}%`
}
