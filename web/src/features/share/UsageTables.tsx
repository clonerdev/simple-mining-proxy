import { useTranslation } from 'react-i18next'
import type { DayUsage } from '../../api/types'
import { StatCard } from '../../components/Panel'
import { locale } from '../../i18n'
import { formatDuration, formatInt } from '../../lib/format'
import { byCustomer, formatPercent, shareOfWork, summarize, type Names } from '../../lib/usage'

/** The totals of the period: operator time, windows, share of work and the operator's shares. */
export function UsageCards({ rows }: { rows: DayUsage[] }) {
  const { t } = useTranslation()
  const loc = locale()
  const total = summarize(rows)
  return (
    <div className="cards">
      <StatCard label={t('share.cardTime')} value={formatDuration(total.allocatedSeconds, t)} sub={t('share.cardTimeSub', { n: formatInt(total.windows, loc) })} />
      <StatCard label={t('share.cardWork')} value={formatPercent(shareOfWork(total), loc)} sub={t('share.cardWorkSub')} />
      <StatCard
        label={t('share.cardShares')}
        value={formatInt(total.operatorAccepted, loc)}
        sub={t('share.cardSharesSub', { n: formatInt(total.operatorRejected, loc) })}
      />
      <StatCard
        label={t('share.cardCustomers')}
        value={formatInt(total.customerAccepted, loc)}
        sub={t('share.cardCustomersSub', { n: formatInt(total.customerRejected, loc) })}
      />
    </div>
  )
}

/** One line per customer for the period. */
export function CustomerUsageTable({ rows, names }: { rows: DayUsage[]; names: Names }) {
  const { t } = useTranslation()
  const loc = locale()
  return (
    <div className="panel table-wrap">
      <table className="responsive">
        <thead>
          <tr>
            <th>{t('share.customer')}</th>
            <th className="num">{t('share.devices')}</th>
            <th className="num">{t('share.windows')}</th>
            <th className="num">{t('share.time')}</th>
            <th className="num">{t('share.operatorShares')}</th>
            <th className="num">{t('share.customerShares')}</th>
            <th className="num">{t('share.work')}</th>
            <th>{t('share.rule')}</th>
          </tr>
        </thead>
        <tbody>
          {byCustomer(rows).map((c) => (
            <tr key={c.customerId}>
              <td className="primary">
                <b>{names.customer(c.customerId)}</b>
              </td>
              <td className="num" data-label={t('share.devices')}>
                {formatInt(c.devices, loc)}
              </td>
              <td className="num" data-label={t('share.windows')}>
                {formatInt(c.windows, loc)}
              </td>
              <td className="num" data-label={t('share.time')}>
                {formatDuration(c.allocatedSeconds, t)}
              </td>
              <td className="num" data-label={t('share.operatorShares')}>
                {formatInt(c.operatorAccepted, loc)}
                {c.operatorRejected > 0 && <span className="muted"> / {formatInt(c.operatorRejected, loc)}</span>}
              </td>
              <td className="num" data-label={t('share.customerShares')}>
                {formatInt(c.customerAccepted, loc)}
                {c.customerRejected > 0 && <span className="muted"> / {formatInt(c.customerRejected, loc)}</span>}
              </td>
              <td className="num" data-label={t('share.work')}>
                {formatPercent(shareOfWork(c), loc)}
              </td>
              <td className="small mono" data-label={t('share.rule')}>
                {c.rules.join(', ') || '—'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** Every device and day: what the CSV holds. */
export function DayUsageTable({ rows, names }: { rows: DayUsage[]; names: Names }) {
  const { t } = useTranslation()
  const loc = locale()
  const sorted = [...rows].sort(
    (a, b) => b.day.localeCompare(a.day) || a.customer_id.localeCompare(b.customer_id) || a.device_id.localeCompare(b.device_id),
  )
  return (
    <div className="panel table-wrap">
      <table className="responsive">
        <thead>
          <tr>
            <th>{t('share.day')}</th>
            <th>{t('share.customer')}</th>
            <th>{t('share.device')}</th>
            <th className="num">{t('share.windows')}</th>
            <th className="num">{t('share.time')}</th>
            <th className="num">{t('share.operatorShares')}</th>
            <th className="num">{t('share.customerShares')}</th>
            <th className="num">{t('share.work')}</th>
          </tr>
        </thead>
        <tbody>
          {sorted.map((r) => (
            <tr key={`${r.day}|${r.device_id}`}>
              <td className="primary">{r.day}</td>
              <td data-label={t('share.customer')}>{names.customer(r.customer_id)}</td>
              <td className="mono small" data-label={t('share.device')}>
                {names.device(r.device_id)}
              </td>
              <td className="num" data-label={t('share.windows')}>
                {formatInt(r.windows, loc)}
              </td>
              <td className="num" data-label={t('share.time')}>
                {formatDuration(r.allocated_seconds, t)}
              </td>
              <td className="num" data-label={t('share.operatorShares')}>
                {formatInt(r.operator_accepted, loc)}
                {r.operator_rejected > 0 && <span className="muted"> / {formatInt(r.operator_rejected, loc)}</span>}
              </td>
              <td className="num" data-label={t('share.customerShares')}>
                {formatInt(r.customer_accepted, loc)}
                {r.customer_rejected > 0 && <span className="muted"> / {formatInt(r.customer_rejected, loc)}</span>}
              </td>
              <td className="num" data-label={t('share.work')}>
                {formatPercent(
                  shareOfWork({ operatorDifficulty: r.operator_difficulty, customerDifficulty: r.customer_difficulty }),
                  loc,
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
