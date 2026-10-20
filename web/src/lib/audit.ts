import type { AuditQuery, AuditResult, AuditTarget } from '../api/types'

// The audit log: what the filters of the screen mean for GET api/audit
// (internal/admin/users.go). Pure functions, so the request is tested without
// a browser.

/** How far back the log is read. The server reads the last 30 days when no start is given. */
export const ranges = ['24h', '7d', '30d', '90d'] as const
export type RangeKey = (typeof ranges)[number]

const hours: Record<RangeKey, number> = { '24h': 24, '7d': 7 * 24, '30d': 30 * 24, '90d': 90 * 24 }

/** The start of a range, counted back from `now`, as RFC 3339 in UTC with whole seconds. */
export function rangeStart(range: RangeKey, now: Date): string {
  const d = new Date(now.getTime() - hours[range] * 3600 * 1000)
  return d.toISOString().replace(/\.\d{3}Z$/, 'Z')
}

/**
 * The groups of actions the filter offers. The server matches the start of the
 * action name, so "device." takes every action on a device.
 */
export const actionGroups = ['auth.', 'access.', 'user.', 'customer.', 'device.', 'pool.', 'settings.', 'miners.', 'profit.', 'timed.', 'telegram.', 'audit.'] as const

export const results: AuditResult[] = ['ok', 'denied', 'failed']

/** What the person asked for on the screen. Empty text means "any". */
export interface AuditFilter {
  range: RangeKey
  action: string
  result: AuditResult | ''
  actor: string
  text: string
}

export const emptyFilter: AuditFilter = { range: '7d', action: '', result: '', actor: '', text: '' }

/** The filter as the query of the API: only what is set is sent. */
export function toQuery(filter: AuditFilter, now: Date): AuditQuery {
  const q: AuditQuery = { from: rangeStart(filter.range, now) }
  if (filter.action) q.action = filter.action
  if (filter.result) q.result = filter.result
  if (filter.actor.trim()) q.actor = filter.actor.trim()
  if (filter.text.trim()) q.q = filter.text.trim()
  return q
}

/** The URL query for a request; `before` pages back to older entries. */
export function queryString(q: AuditQuery, extra: Record<string, string> = {}): string {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries({ ...q, ...extra })) {
    if (v !== undefined && v !== '') p.set(k, String(v))
  }
  return p.toString()
}

/** Entries asked for at a time. */
export const pageSize = 200

/** The most rows one CSV export holds (the server's limit). */
export const csvRows = 1000

/** The key of an action group in the texts: "device." is "device". */
export const groupKey = (group: string): string => group.replace(/\.$/, '')

/** What an entry acted on, for a table cell: "device: acme.rack1"; empty when it names nothing. */
export function targetText(target: AuditTarget | undefined): string {
  if (!target) return ''
  return [target.type, target.name || target.id].filter(Boolean).join(': ')
}

/** The look of a result: a refused or failed operation stands out from the ones that went through. */
export const resultClass = (result: AuditResult): string => (result === 'ok' ? 'ok' : result === 'denied' ? 'warn' : 'bad')
