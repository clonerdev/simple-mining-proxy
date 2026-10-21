import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useCustomers, useDevices, useOperatorUsage } from '../api/queries'
import { QueryState } from '../components/Panel'
import { ShareStatus } from '../features/share/ShareStatus'
import { CustomerUsageTable, DayUsageTable, UsageCards } from '../features/share/UsageTables'
import { deviceLabel } from '../lib/deviceActions'
import { downloadText } from '../lib/download'
import { ranges, usageCsv, usageRange, type Names, type UsageRange } from '../lib/usage'

/**
 * The account of the operator's share: how much of the work was credited to the
 * operator and how much to each customer, from the shares the pool answered.
 * It is work, not money: the pool pays whoever a share was credited to.
 */
export function SharePage() {
  const { t } = useTranslation()
  const [range, setRange] = useState<UsageRange>('30d')
  const [customer, setCustomer] = useState('')
  // The days are fixed when the choice is made, so that they do not move with every render.
  const [now, setNow] = useState(() => new Date())
  const { from, to } = useMemo(() => usageRange(range, now), [range, now])
  const usage = useOperatorUsage(from, to, customer)
  const customers = useCustomers()
  const devices = useDevices()
  const rows = useMemo(() => usage.data?.rows ?? [], [usage.data])

  // Names come from the customer and device lists when the account may read them; the ids stand in otherwise.
  const names: Names = useMemo(() => {
    const c = new Map((customers.data?.customers ?? []).map((x) => [x.id, x.name]))
    const d = new Map((devices.data?.devices ?? []).map((x) => [x.id, deviceLabel(x)]))
    return { customer: (id) => c.get(id) ?? id, device: (id) => d.get(id) ?? id }
  }, [customers.data, devices.data])

  const choices = useMemo(() => {
    const known = customers.data?.customers.map((c) => ({ id: c.id, name: c.name }))
    if (known?.length) return known
    return [...new Set(rows.map((r) => r.customer_id))].map((id) => ({ id, name: id }))
  }, [customers.data, rows])

  const choose = (next: UsageRange) => {
    setRange(next)
    setNow(new Date())
  }

  return (
    <>
      <h2>{t('share.title')}</h2>
      <p className="small muted">{t('share.help')}</p>

      <ShareStatus />

      <h3>{t('share.usage')}</h3>
      <div className="toolbar">
        <div className="chips" role="group" aria-label={t('share.period')}>
          {ranges.map((r) => (
            <button
              key={r}
              type="button"
              className={r === range ? 'chip active' : 'chip'}
              aria-pressed={r === range}
              onClick={() => choose(r)}
            >
              {t(`share.ranges.${r}`)}
            </button>
          ))}
        </div>
        <select aria-label={t('share.customer')} value={customer} onChange={(e) => setCustomer(e.target.value)}>
          <option value="">{t('share.allCustomers')}</option>
          {choices.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
        <button
          type="button"
          disabled={!rows.length}
          title={t('share.csvHelp')}
          onClick={() => downloadText(`operator-share_${from}_${to}.csv`, usageCsv(rows, names))}
        >
          {t('share.csv')}
        </button>
      </div>
      <p className="small muted">{t('share.days', { from, to })}</p>

      <QueryState error={usage.error} pending={usage.isPending}>
        {rows.length ? (
          <>
            <UsageCards rows={rows} />
            <h3>{t('share.byCustomer')}</h3>
            <CustomerUsageTable rows={rows} names={names} />
            <details className="share-days">
              <summary>{t('share.byDay', { n: rows.length })}</summary>
              <DayUsageTable rows={rows} names={names} />
            </details>
          </>
        ) : (
          <div className="panel">
            <p>{t('share.empty')}</p>
            <p className="small muted">{t('share.emptyHelp')}</p>
          </div>
        )}
      </QueryState>
    </>
  )
}
