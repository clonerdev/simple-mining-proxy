import { csvRows, queryString } from '../lib/audit'
import type {
  ActivateResult,
  ApiErrorBody,
  AuditEntry,
  AuditQuery,
  Customer,
  CustomerInput,
  CustomerList,
  CustomerStatus,
  Device,
  DeviceAction,
  DeviceList,
  DeviceStatus,
  DrainResult,
  EventItem,
  EventLevel,
  History,
  Me,
  Miner,
  Msg,
  NetworkStatus,
  OperatorPools,
  OperatorUsage,
  Pool,
  PoolInput,
  ProfitStatus,
  SettingsPayload,
  SettingsSaved,
  SettingValue,
  Status,
  TelegramStatus,
  TestResult,
  TimedStatus,
  WorkerSeries,
  WorkerSummary,
} from './types'

/** A failed API call with the server's message and per-field messages. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly msg: Msg
  readonly fields: Record<string, Msg>

  constructor(status: number, body: Partial<ApiErrorBody> | null) {
    const msg: Msg = body?.key
      ? { key: body.key, params: body.params, message: body.message ?? '' }
      : { key: '', message: body?.message ?? `HTTP ${status}` }
    super(msg.message)
    this.name = 'ApiError'
    this.status = status
    this.code = body?.error ?? ''
    this.msg = msg
    this.fields = body?.fields ?? {}
  }
}

type Method = 'GET' | 'POST' | 'PUT' | 'DELETE'

// Paths are relative so the UI also works behind a reverse proxy prefix.
async function request<T>(method: Method, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: 'same-origin' }
  if (method !== 'GET') {
    // The API requires JSON for every state-changing request (CSRF guard).
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body ?? {})
  }
  const res = await fetch(path, init)
  const data: unknown = await res.json().catch(() => null)
  if (!res.ok) throw new ApiError(res.status, data as Partial<ApiErrorBody> | null)
  return data as T
}

const pool = (id: string) => `api/pools/${encodeURIComponent(id)}`
const customer = (id: string) => `api/customers/${encodeURIComponent(id)}`
const device = (id: string) => `api/devices/${encodeURIComponent(id)}`

export const api = {
  me: () => request<Me>('GET', 'api/auth/me'),
  login: (username: string, password: string) =>
    request<{ username: string }>('POST', 'api/auth/login', { username, password }),
  logout: () => request<{ ok: boolean }>('POST', 'api/auth/logout'),

  status: () => request<Status>('GET', 'api/status'),
  miners: () => request<{ miners: Miner[] }>('GET', 'api/miners').then((r) => r.miners),
  events: (limit: number, level: EventLevel = 'info') =>
    request<{ events: EventItem[] }>('GET', `api/events?limit=${limit}&level=${level}`).then((r) => r.events),
  reconnectAll: () => request<DrainResult>('POST', 'api/miners/reconnect'),

  pools: () => request<{ pools: Pool[] }>('GET', 'api/pools').then((r) => r.pools),
  createPool: (input: PoolInput) => request<Pool>('POST', 'api/pools', input),
  updatePool: (id: string, input: PoolInput, reconnect: boolean) =>
    request<{ pool: Pool; reconnecting: number }>('PUT', `${pool(id)}${reconnect ? '?reconnect=true' : ''}`, input),
  deletePool: (id: string) => request<{ reconnecting: number }>('DELETE', pool(id)),
  testPool: (id: string) => request<TestResult>('POST', `${pool(id)}/test`),
  /** Checks editor fields before saving; id names the saved pool they apply to. */
  testConfig: (input: PoolInput) => request<TestResult>('POST', 'api/pools/test', input),
  activatePool: (id: string, force: boolean) =>
    request<ActivateResult>('POST', `${pool(id)}/activate${force ? '?force=true' : ''}`),
  setFallback: (ids: string[]) => request<{ pools: Pool[] }>('PUT', 'api/fallback', { pools: ids }),

  /** The last `seconds` of statistics in at most about `points` steps. */
  history: (seconds: number, points: number) => {
    const to = Math.floor(Date.now() / 1000)
    return request<History>('GET', `api/history?from=${to - seconds}&to=${to}&points=${points}`)
  },

  /** One ASIC login over the last `seconds`. */
  workerHistory: (name: string, seconds: number, points: number) => {
    const to = Math.floor(Date.now() / 1000)
    return request<{ name: string; series: WorkerSeries }>(
      'GET',
      `api/history/worker?name=${encodeURIComponent(name)}&from=${to - seconds}&to=${to}&points=${points}`,
    )
  },
  /** Every ASIC login seen over the last `seconds`, connected now or not. */
  workers: (seconds: number) => {
    const to = Math.floor(Date.now() / 1000)
    return request<{ workers: WorkerSummary[] }>('GET', `api/history/workers?from=${to - seconds}&to=${to}`).then(
      (r) => r.workers,
    )
  },

  profit: () => request<ProfitStatus>('GET', 'api/profit'),
  /** Compares the coins now; never switches. */
  profitCheck: () => request<ProfitStatus>('POST', 'api/profit/check'),
  timed: () => request<TimedStatus>('GET', 'api/timed'),
  network: () => request<NetworkStatus>('GET', 'api/network'),

  settings: () => request<SettingsPayload>('GET', 'api/settings'),
  /** null resets a setting to its default. */
  saveSettings: (changes: Record<string, SettingValue | null>) =>
    request<SettingsSaved>('PUT', 'api/settings', changes),
  /** The main pool profit switching returns to; "" for none. */
  setProfitHome: (pool: string) => request<ProfitStatus>('PUT', 'api/profit/home', { pool }),
  /** Restarts the timer schedule now: the farm goes to the timer pool at once. */
  timedStart: () => request<TimedStatus>('POST', 'api/timed/start'),
  /** Sets the bot token after Telegram confirms it; "" removes it. */
  setTelegramToken: (token: string) => request<TelegramStatus>('PUT', 'api/telegram/token', { token }),
  testTelegram: () => request<TelegramStatus>('POST', 'api/telegram/test'),

  customers: () => request<CustomerList>('GET', 'api/customers'),
  /** The connection key is in the answer once: only its hash is kept. */
  createCustomer: (input: CustomerInput) => request<{ customer: Customer; key: string }>('POST', 'api/customers', input),
  updateCustomer: (id: string, input: Partial<CustomerInput>) => request<Customer>('PUT', customer(id), input),
  setCustomerStatus: (id: string, status: CustomerStatus, reason: string) =>
    request<Customer>('POST', `${customer(id)}/status`, { status, reason }),
  rotateCustomerKey: (id: string) => request<{ key: string }>('POST', `${customer(id)}/key`),
  /** The administrator records (grant) or withdraws the customer's consent to the operator's share. */
  setFeeConsent: (id: string, grant: boolean, terms: string, note: string) =>
    request<Customer>('POST', `${customer(id)}/fee-consent`, { grant, terms, note }),
  deleteCustomer: (id: string) => request<{ ok: boolean }>('DELETE', customer(id)),

  /** status and customer narrow the list; "" means all. */
  devices: (status: DeviceStatus | '' = '', customerId = '') => {
    const q = new URLSearchParams()
    if (status) q.set('status', status)
    if (customerId) q.set('customer', customerId)
    const query = q.toString()
    return request<DeviceList>('GET', `api/devices${query ? `?${query}` : ''}`)
  },
  /** expiresAt (RFC 3339) ends the device's access; allowed with approve and resume only. */
  deviceAction: (id: string, action: DeviceAction, reason = '', expiresAt?: string) =>
    request<Device>('POST', `${device(id)}/action`, { action, reason, ...(expiresAt ? { expires_at: expiresAt } : {}) }),
  /** expires_at "" clears the end; absent leaves it. */
  updateDevice: (id: string, input: { expires_at?: string; max_connections?: number; notes?: string }) =>
    request<Device>('PUT', device(id), input),
  deleteDevice: (id: string) => request<{ ok: boolean }>('DELETE', device(id)),

  /** Newest first; `before` (the time of the oldest entry shown) asks for the next older ones. */
  audit: (q: AuditQuery) => request<{ entries: AuditEntry[] }>('GET', `api/audit?${queryString(q)}`),
  /** Where the browser downloads the log as CSV: the newest rows that match, up to the server's limit. */
  auditCsvUrl: (q: AuditQuery) => `api/audit?${queryString({ ...q, before: undefined }, { format: 'csv', limit: String(csvRows) })}`,

  /** The account of the operator's share, one row per device and UTC day (2006-01-02, both ends included). */
  operatorUsage: (from: string, to: string, customerId = '') => {
    const q = new URLSearchParams({ from, to })
    if (customerId) q.set('customer', customerId)
    return request<OperatorUsage>('GET', `api/operator-share/usage?${q.toString()}`)
  },
  /** The proxy-wide switch of the share and the pools it switched itself off on. */
  operatorPools: () => request<OperatorPools>('GET', 'api/operator-share/pools'),
}
