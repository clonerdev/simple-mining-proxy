import type { ShareKind, ShareRule } from '../api/types'

export const shareKinds: ShareKind[] = ['percent', 'minutes_per_day']

/** The largest amount of each kind: the whole of the mining time. */
export const shareMax: Record<ShareKind, number> = { percent: 100, minutes_per_day: 24 * 60 }

/** The share of the mining time a rule gives, 0 to 1; zero for a rule the server would refuse. */
export function shareFraction(rule: ShareRule): number {
  if (!validShare(rule.kind, rule.amount)) return 0
  return rule.kind === 'minutes_per_day' ? rule.amount / shareMax.minutes_per_day : rule.amount / 100
}

export function validShare(kind: ShareKind, amount: number): boolean {
  return shareKinds.includes(kind) && Number.isFinite(amount) && amount >= 0 && amount <= shareMax[kind]
}

/** A number typed in a field: "2.5" or "2,5"; null when it is empty, not a plain number or negative. */
export function parseAmount(text: string): number | null {
  const s = text.trim().replace(',', '.')
  if (!/^\d{1,4}(\.\d{1,2})?$/.test(s)) return null
  return Number(s)
}

/** The amount for a text field, without trailing zeros. */
export function amountText(amount: number): string {
  return String(Number(amount.toFixed(2)))
}

/**
 * Where a device's share comes from: its own rule, else its customer's, else
 * the proxy-wide setting. The server decides the same way on every share.
 */
export function effectiveLevel(device: { operator_share?: ShareRule }, customer?: { operator_share?: ShareRule }): 'device' | 'customer' | 'setting' {
  if (device.operator_share) return 'device'
  if (customer?.operator_share) return 'customer'
  return 'setting'
}
