import { useTranslation } from 'react-i18next'
import type { Device, DeviceAction } from '../../api/types'
import { actionsFor, canDelete, deviceLabel, isDanger } from '../../lib/deviceActions'
import { formatAgo, formatHashrateHs } from '../../lib/format'
import { DeviceStatusBadge, Expiry } from './Status'

interface Props {
  devices: Device[]
  /** Whether the signed-in account may change devices (the server checks again). */
  canManage: boolean
  onAction: (d: Device, action: DeviceAction) => void
  onEdit: (d: Device) => void
  onDelete: (d: Device) => void
}

/** Devices with the actions an administrator can take on each state. */
export function DevicesTable({ devices, canManage, onAction, onEdit, onDelete }: Props) {
  const { t } = useTranslation()
  return (
    <div className="panel table-wrap">
      <table className="responsive">
        <thead>
          <tr>
            <th>{t('devices.device')}</th>
            <th>{t('devices.state')}</th>
            <th>{t('devices.ends')}</th>
            <th>{t('devices.lastSeen')}</th>
            <th className="num">{t('devices.hashrate')}</th>
            <th>
              <span className="sr-only">{t('devices.actions')}</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {devices.map((d) => (
            <tr key={d.id}>
              <td className="primary">
                <b className="mono">{deviceLabel(d)}</b>
                <div className="small muted">
                  {d.customer_name}
                  {d.last_ip ? ` · ${d.last_ip}` : ''}
                </div>
                {d.notes && <div className="small muted">{d.notes}</div>}
              </td>
              <td data-label={t('devices.state')}>
                <DeviceStatusBadge device={d} />
                {d.status_reason && <div className="small muted">{d.status_reason}</div>}
              </td>
              <td data-label={t('devices.ends')}>
                {d.status === 'pending' || d.status === 'rejected' ? <span className="muted">—</span> : <Expiry iso={d.expires_at} />}
              </td>
              <td data-label={t('devices.lastSeen')}>
                {d.live.sessions > 0 ? <span className="badge ok">{t('devices.online')}</span> : formatAgo(d.last_seen, t)}
              </td>
              <td className="num" data-label={t('devices.hashrate')}>
                {d.live.sessions > 0 ? formatHashrateHs(d.live.hashrate_hs) : '—'}
              </td>
              <td className="actions-cell">
                {canManage && (
                  <div className="actions">
                    {actionsFor(d.status).map((a) => (
                      <button
                        key={a}
                        type="button"
                        className={isDanger(a) ? 'small danger' : a === 'approve' ? 'small primary' : 'small'}
                        onClick={() => onAction(d, a)}
                      >
                        {t(`devices.action.${a}`)}
                      </button>
                    ))}
                    <button type="button" className="small" onClick={() => onEdit(d)}>
                      {t('devices.edit')}
                    </button>
                    {canDelete(d.status) && (
                      <button type="button" className="small danger" onClick={() => onDelete(d)}>
                        {t('devices.delete')}
                      </button>
                    )}
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
