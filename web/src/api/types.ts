// Shapes of the admin API responses (internal/admin/server.go).

export type Health = 'UP' | 'DOWN' | 'UNKNOWN'
export type Mode = 'NORMAL' | 'DEGRADED' | 'DOWN' | 'SWITCHING'
export type Role = 'active' | 'fallback' | 'none'

/** An operator-facing message: the UI translates key with params; message is the English text. */
export interface Msg {
  key: string
  params?: Record<string, unknown>
  message: string
}

export interface ApiErrorBody extends Msg {
  error: string
  fields?: Record<string, Msg>
}

export interface ConnString {
  enabled: boolean
  listen: string
  scheme: string
  host: string
  port: string
  url?: string
}

export interface Connections {
  tcp: ConnString
  tls: ConnString
}

export interface Status {
  uptime_seconds: number
  mode: Mode
  active_pool: string
  effective_pool: string
  fallback_pools: string[]
  last_switch: { at: string; from: string; to: string; reason: string } | null
  miners: { total: number; tcp: number; tls: number }
  shares: { accepted: number; rejected: number; reject_reasons: Record<string, number> }
  hashrate_ths: number
  connections: Connections
  errors: { tls_handshake: number; upstream_invalid_json: number }
}

export interface Address {
  host: string
  port: number
}

export interface PoolAddress extends Address {
  health: Health
  /** Median TCP connect time of the last probes. */
  latency_ms: number | null
  last_error?: string
  /** New sessions try this address first. */
  preferred: boolean
}

export interface Pool {
  id: string
  name: string
  coin: string
  addresses: PoolAddress[]
  tls: boolean
  tls_skip_verify: boolean
  username: string
  password_set: boolean
  /** Profit switching may choose this pool. */
  profit_switch: boolean
  /** Timed switching moves the farm here for part of every period; one pool at most. */
  timed_target: boolean
  /** A solo pool: pays only for blocks the farm finds. Any role, but never profit switching. */
  solo: boolean
  /** The main pool profit switching returns to; one pool at most. */
  profit_home: boolean
  role: Role
  fallback_position?: number
  health: Health
  last_check: string | null
  last_error: string | null
  sessions: number
  accepted: number
  rejected: number
  hashrate_ths: number
}

/** Pool create/update body; omitted fields stay unchanged. */
export interface PoolInput {
  id?: string
  name?: string
  coin?: string
  addresses?: Address[]
  tls?: boolean
  tls_skip_verify?: boolean
  username?: string
  password?: string
  profit_switch?: boolean
  timed_target?: boolean
  solo?: boolean
}

export interface TestResult {
  ok: boolean
  elapsed_ms: number
  addresses: (Address & { ok: boolean; latency_ms: number | null; error?: string })[]
}

export interface Miner {
  id: number
  worker: string
  upstream_user: string
  ip: string
  transport: 'tcp' | 'tls'
  pool_id: string
  pool_name: string
  pool_addr: string
  user_agent: string
  difficulty: number
  accepted: number
  rejected: number
  last_share: string | null
  last_submit: string | null
  hashrate_hs: number
  connected_at: string
}

export type EventLevel = 'info' | 'warn' | 'error'

export interface EventItem {
  seq: number
  time: string
  level: EventLevel
  type: string
  message: string
}

export type SettingType = 'duration' | 'int' | 'enum' | 'string'
export type Applies = 'immediately' | 'new_connections' | 'next_drain'
/** Durations are Go duration strings ("20s", "2m"). */
export type SettingValue = string | number

export interface SettingMeta {
  key: string
  group: string
  type: SettingType
  unit?: string
  value: SettingValue
  default: SettingValue
  min?: SettingValue
  max?: SettingValue
  options?: string[]
  min_len?: number
  max_len?: number
  applies: Applies
  modified: boolean
}

export interface CertInfo {
  type: 'self-signed' | 'loaded'
  sha256: string
  subject: string
  dns_names: string[] | null
  not_after: string
}

export interface ServerInfo {
  connections: Connections
  public_host: string
  admin_listen: string
  admin_username: string
  api_token_set: boolean
  data_dir: string
  log_format: string
  certificate: CertInfo | null
  telegram: TelegramStatus
}

/** The Telegram bot for alerts; configured means a bot token is set. The token itself is never returned. */
export interface TelegramStatus {
  configured: boolean
  username?: string
  error?: string
  chats: number
  /** The token is set by TELEGRAM_BOT_TOKEN: it cannot be changed from the panel. */
  token_from_env: boolean
}

export interface SettingsPayload {
  groups: string[]
  settings: SettingMeta[]
  modified: number
  server: ServerInfo
}

export interface SettingChange {
  key: string
  old: string
  new: string
  applies: Applies
}

export interface SettingsSaved extends SettingsPayload {
  changes: SettingChange[]
  reconnect_suggested: boolean
  sessions: number
}

export interface DrainResult {
  sessions: number
  window: string
}

export interface ActivateResult {
  reconnecting: number
  window: string
}

/** Statistics resampled to a fixed step; null is a step without data. */
export interface HistorySeries {
  step: number
  time: number[]
  hashrate: (number | null)[] // H/s
  accepted: (number | null)[]
  rejected: (number | null)[]
  miners: (number | null)[]
  pools: Record<string, (number | null)[]> // hashrate per pool id, H/s
}

export interface History {
  from: number
  to: number
  series: HistorySeries
  pool_names: Record<string, string>
  detail_retention: string
  retention: string
  miner_retention: string
  disk_bytes: number
}

/** One ASIC login over time; online is the part of each step it was connected (0–1). */
export interface WorkerSeries {
  step: number
  time: number[]
  hashrate: (number | null)[]
  accepted: (number | null)[]
  rejected: (number | null)[]
  online: (number | null)[]
  /** The whole range, computed like the miners list on the Stats page. */
  summary: WorkerSummary
}

export interface WorkerSummary {
  name: string
  /** H/s averaged over the time it was connected. */
  hashrate: number
  accepted: number
  rejected: number
  /** Part of the recorded time it was connected, 0–1. */
  online: number
  /** Unix seconds: end of the last interval it was connected or sent a share; 0 if never. */
  last_seen: number
}

export type ProfitMode = 'off' | 'advise' | 'auto'

export type ProfitDecision =
  | 'no_data'
  | 'no_candidates'
  | 'manual'
  | 'best'
  | 'below_margin'
  | 'recommend'
  | 'switched'
  | 'switch_failed'
  | 'away_stay'
  | 'return_home'
  | 'returned'

/** One coin; revenue is for 1 TH/s over a day. */
export interface CoinView {
  tag: string
  name: string
  price_btc: number
  price_usd: number
  /** 24-hour average network difficulty. */
  difficulty: number
  /** Latest network difficulty. */
  difficulty_now: number
  block_reward: number
  revenue_btc: number
  revenue_usd: number
  stale: boolean
  /** Share of the block rate the difficulty suggests that miners really get: below 1 on eCash (Real Time Targeting). */
  efficiency: number
  pools: string[]
}

export interface ProfitReport {
  at: string
  scheduled: boolean
  mode: ProfitMode
  margin: number
  coins: CoinView[]
  active: string
  active_coin: string
  best: string
  advantage: number
  decision: ProfitDecision
  target?: string
  error?: string
  /** The main pool the farm returns to, and its coin; absent without one. */
  home?: string
  home_coin?: string
  return_margin: number
  /** Away from the main pool: percent the active coin earns over the main pool's. */
  over_home: number
}

export interface ProfitStatus {
  mode: ProfitMode
  interval: string
  margin: number
  last_run: string | null
  next_run: string | null
  btc_usd: number
  report: ProfitReport | null
}

/** Market data for the solo odds; coins carry no pools here. */
export interface NetworkStatus {
  fetched: string | null
  btc_usd: number
  coins: CoinView[]
  error?: string
  /** eCash seen live through an eCash pool; null without one. */
  rtt: RttStatus | null
}

export interface RttStatus {
  pool: string
  connected: boolean
  /** Difficulty of the block being mined, from its header target. */
  difficulty: number
  /** How many times harder the real-time target is now; 1 when not known. */
  factor: number
  last_block: string | null
  error?: string
}

export interface TimedStatus {
  mode: 'off' | 'on'
  /** Go durations, e.g. "30m". */
  period: string
  duration: string
  /** Pool id marked for timed switching; "" when none is. */
  target: string
  /** While the farm is on the target: the pool it returns to and when. */
  home?: string
  until?: string
  /** Start of the next period. */
  next?: string
  /** Seconds on the target still due in this period. */
  left: number
  /** Held off: a block just arrived on the target's chain (eCash). */
  waiting: boolean
  /** How many times harder a block on the target is now. */
  hardness: number
  /** Why the last switch to the target failed. */
  error?: string
  hunt: HuntStatus
}

/** Block hunting on eCash: the solo pool while a block is not harder than usual, home at every block. */
export interface HuntStatus {
  mode: 'off' | 'on'
  /** The eCash solo pool; "" when there is none. */
  target: string
  /** eCash blocks are seen as they arrive; without that hunting waits. */
  live: boolean
  /** How many times harder than its header a block is now. */
  hardness: number
  /** Hunting now: since when and the pool it returns to. */
  since?: string
  home?: string
  /** A pool was chosen by hand: hunting waits for the next block. */
  hold: boolean
  /** The last 24 hours, the current hunt too. */
  stints: number
  seconds: number
  /** Why the last switch to the hunt pool failed. */
  error?: string
  /** Paused after that failure until then. */
  retry?: string
}

// ---- customers and devices (internal/admin/devices.go) ----

export type DeviceStatus = 'pending' | 'approved' | 'rejected' | 'suspended' | 'blocked' | 'revoked'
export type CustomerStatus = 'active' | 'suspended' | 'blocked'
export type DeviceAction = 'approve' | 'reject' | 'reopen' | 'suspend' | 'resume' | 'block' | 'unblock' | 'revoke'

/** What the Stratum side knows of a customer's or a device's connections right now. */
export interface LiveStats {
  sessions: number
  hashrate_hs: number
  accepted: number
  rejected: number
}

export interface FeeConsent {
  at: string
  by: string
  terms?: string
  note?: string
}

export interface Customer {
  id: string
  slug: string
  name: string
  status: CustomerStatus
  status_reason?: string
  status_at?: string
  account?: string
  max_devices: number
  max_connections: number
  expires_at?: string
  language?: string
  notes?: string
  fee_consent?: FeeConsent
  key_rotated_at: string
  created_at: string
  updated_at: string
  devices: Partial<Record<DeviceStatus, number>>
  live: LiveStats
}

export interface DeviceChange {
  at: string
  from: DeviceStatus
  to: DeviceStatus
  by: string
  reason?: string
}

export interface Device {
  id: string
  customer_id: string
  customer_slug: string
  customer_name: string
  worker: string
  upstream_worker: string
  status: DeviceStatus
  status_reason?: string
  status_by?: string
  status_at: string
  expires_at?: string
  max_connections: number
  notes?: string
  user_agent?: string
  last_ip?: string
  first_seen: string
  last_seen: string
  denied?: number
  history?: DeviceChange[]
  live: LiveStats
  /** Whether the device may mine at this moment, and if not, why (a short key such as device_expired). */
  usable: boolean
  why?: string
}

export interface DeviceCounts {
  customers: Partial<Record<CustomerStatus, number>>
  devices: Partial<Record<DeviceStatus, number>>
}

export interface CustomerList {
  customers: Customer[]
  counts: DeviceCounts
  /** Whether device control is switched on; when it is off nothing below is enforced. */
  control: boolean
  languages: string[]
}

export interface DeviceList {
  devices: Device[]
  counts: DeviceCounts
}

export interface CustomerInput {
  slug?: string
  name: string
  account: string
  max_devices: number
  max_connections: number
  /** RFC 3339; "" clears it; absent leaves it as it is (when editing). */
  expires_at?: string
  language: string
  notes: string
}

/** The signed-in account (GET api/auth/me). */
export interface Me {
  username: string
  role: string
  permissions: string[]
  language?: string
}

// ---- audit log (internal/audit, internal/admin/users.go) ----

export type AuditResult = 'ok' | 'denied' | 'failed'

export interface AuditActor {
  /** user, env_admin, api_token, system, telegram, device or anonymous */
  type: string
  name?: string
  id?: string
  ip?: string
}

export interface AuditTarget {
  type?: string
  id?: string
  name?: string
}

export interface AuditEntry {
  id: string
  time: string
  actor: AuditActor
  /** Dotted, such as device.approve; the start of it names the group. */
  action: string
  target?: AuditTarget
  result: AuditResult
  message?: string
  /** Secrets are already replaced by the server. */
  details?: Record<string, unknown>
}

/** The filters of GET api/audit. Times are RFC 3339; before pages back to older entries. */
export interface AuditQuery {
  from?: string
  to?: string
  before?: string
  actor?: string
  action?: string
  target?: string
  result?: AuditResult
  q?: string
  limit?: number
}
