import { useTranslation } from 'react-i18next'
import type { Customer, DeviceStatus } from '../../api/types'
import { statusOrder } from '../../lib/deviceActions'
import { formatHashrateHs } from '../../lib/format'
import { CustomerStatusBadge, Expiry } from './Status'

interface Props {
  customers: Customer[]
  canManage: boolean
  onEdit: (c: Customer) => void
  onState: (c: Customer) => void
  onConsent: (c: Customer) => void
  onKey: (c: Customer) => void
  onDelete: (c: Customer) => void
}

/** "3 active · 1 pending": the devices of a customer by state, empty ones left out. */
function Counts({ devices }: { devices: Customer['devices'] }) {
  const { t } = useTranslation()
  const parts = statusOrder
    .filter((s: DeviceStatus) => (devices[s] ?? 0) > 0)
    .map((s) => `${devices[s]} ${t(`devices.status.${s}`)}`)
  return <>{parts.length ? parts.join(' · ') : <span className="muted">—</span>}</>
}

export function CustomersTable({ customers, canManage, onEdit, onState, onConsent, onKey, onDelete }: Props) {
  const { t } = useTranslation()
  return (
    <div className="panel table-wrap">
      <table className="responsive">
        <thead>
          <tr>
            <th>{t('devices.customer')}</th>
            <th>{t('devices.state')}</th>
            <th>{t('devices.devices')}</th>
            <th>{t('devices.limits')}</th>
            <th>{t('devices.ends')}</th>
            <th>{t('devices.consent')}</th>
            <th className="num">{t('devices.hashrate')}</th>
            <th>
              <span className="sr-only">{t('devices.actions')}</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {customers.map((c) => (
            <tr key={c.id}>
              <td className="primary">
                <b>{c.name}</b>
                <div className="small mono muted">{c.slug}</div>
                {c.account && (
                  <div className="small muted">
                    {t('devices.account')}: <span className="mono">{c.account}</span>
                  </div>
                )}
                {c.notes && <div className="small muted">{c.notes}</div>}
              </td>
              <td data-label={t('devices.state')}>
                <CustomerStatusBadge customer={c} />
                {c.status_reason && <div className="small muted">{c.status_reason}</div>}
              </td>
              <td data-label={t('devices.devices')}>
                <Counts devices={c.devices} />
              </td>
              <td data-label={t('devices.limits')} className="small">
                <div>{t('devices.limitDevices', { value: c.max_devices || '∞' })}</div>
                <div>{t('devices.limitConnections', { value: c.max_connections || '∞' })}</div>
              </td>
              <td data-label={t('devices.ends')}>
                <Expiry iso={c.expires_at} />
              </td>
              <td data-label={t('devices.consent')}>
                {c.fee_consent ? <span className="badge ok">{t('devices.consentYes')}</span> : <span className="muted">{t('devices.consentNo')}</span>}
              </td>
              <td className="num" data-label={t('devices.hashrate')}>
                {c.live.sessions > 0 ? formatHashrateHs(c.live.hashrate_hs) : '—'}
              </td>
              <td className="actions-cell">
                {canManage && (
                  <div className="actions">
                    <button type="button" className="small" onClick={() => onEdit(c)}>
                      {t('devices.edit')}
                    </button>
                    <button type="button" className="small" onClick={() => onState(c)}>
                      {t('devices.state')}
                    </button>
                    <button type="button" className="small" onClick={() => onConsent(c)}>
                      {t('devices.consent')}
                    </button>
                    <button type="button" className="small" onClick={() => onKey(c)}>
                      {t('devices.newKey')}
                    </button>
                    <button type="button" className="small danger" onClick={() => onDelete(c)}>
                      {t('devices.delete')}
                    </button>
                  </div>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
