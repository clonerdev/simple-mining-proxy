import { useTranslation } from 'react-i18next'
import type { Customer, Device, DeviceStatus } from '../../api/types'
import { useNow } from '../../hooks/useNow'
import { locale } from '../../i18n'
import { expiryState } from '../../lib/expiry'
import { formatLongDuration } from '../../lib/format'

const statusClass: Record<DeviceStatus, string> = {
  pending: 'warn',
  approved: 'ok',
  rejected: '',
  suspended: 'warn',
  blocked: 'bad',
  revoked: 'bad',
}

/**
 * The state of a device. An approved device that cannot mine right now (its
 * time is over, or its customer was disabled) shows that instead, because it
 * is what the administrator needs to see.
 */
export function DeviceStatusBadge({ device }: { device: Device }) {
  const { t, i18n } = useTranslation()
  if (device.status === 'approved' && !device.usable && device.why) {
    const key = `devices.why.${device.why}`
    return <span className="badge bad">{i18n.exists(key) ? t(key) : device.why}</span>
  }
  return <span className={`badge ${statusClass[device.status]}`.trim()}>{t(`devices.status.${device.status}`)}</span>
}

export function CustomerStatusBadge({ customer }: { customer: Customer }) {
  const { t } = useTranslation()
  const cls = customer.status === 'active' ? 'ok' : customer.status === 'blocked' ? 'bad' : 'warn'
  return <span className={`badge ${cls}`}>{t(`devices.customerStatus.${customer.status}`)}</span>
}

/** An end time: the date, and how long is left or since it passed. */
export function Expiry({ iso }: { iso?: string | null }) {
  const { t } = useTranslation()
  const now = useNow()
  if (!iso) return <span className="muted">{t('devices.noEnd')}</span>
  const state = expiryState(iso, now)
  const date = new Date(iso).toLocaleString(locale(), { dateStyle: 'medium', timeStyle: 'short' })
  const span = formatLongDuration(state.kind === 'none' ? 0 : Math.max(60, state.ms / 1000), t, locale())
  return (
    <>
      {date}
      <div className={state.kind === 'expired' ? 'small bad-text' : state.kind === 'soon' ? 'small warn-text' : 'small muted'}>
        {state.kind === 'expired' ? t('devices.endedAgo', { value: span }) : t('devices.left', { value: span })}
      </div>
    </>
  )
}
