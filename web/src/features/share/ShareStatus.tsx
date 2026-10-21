import { useTranslation } from 'react-i18next'
import { useOperatorPools, usePools } from '../../api/queries'
import { QueryState } from '../../components/Panel'
import { locale } from '../../i18n'

/** Whether the share is on, and the pools it switched itself off on, with the reason. */
export function ShareStatus() {
  const { t } = useTranslation()
  const loc = locale()
  const status = useOperatorPools()
  const pools = usePools()
  const name = (id: string) => pools.data?.find((p) => p.id === id)?.name ?? id
  const when = (iso?: string) =>
    iso ? new Date(iso).toLocaleString(loc, { dateStyle: 'medium', timeStyle: 'short' }) : '—'

  return (
    <QueryState error={status.error} pending={status.isPending}>
      {status.data && (
        <div className="panel">
          <div className="split">
            <b>{t('share.switch')}</b>
            <span className={status.data.active ? 'badge ok' : 'badge'}>{status.data.active ? t('share.on') : t('share.off')}</span>
          </div>
          <p className="small muted">{status.data.active ? t('share.onHelp') : t('share.offHelp')}</p>

          <b>{t('share.poolsOff')}</b>
          {status.data.pools_off.length ? (
            <table className="responsive">
              <thead>
                <tr>
                  <th>{t('share.pool')}</th>
                  <th>{t('share.since')}</th>
                  <th>{t('share.until')}</th>
                  <th>{t('share.reason')}</th>
                </tr>
              </thead>
              <tbody>
                {status.data.pools_off.map((p) => (
                  <tr key={p.pool_id}>
                    <td className="primary">
                      <b>{name(p.pool_id)}</b>
                    </td>
                    <td data-label={t('share.since')}>{when(p.since)}</td>
                    <td data-label={t('share.until')}>{when(p.until)}</td>
                    <td data-label={t('share.reason')} className="small">
                      {p.reason || '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <p className="small muted">{t('share.poolsOffNone')}</p>
          )}
          <p className="small muted">{t('share.poolsOffHelp')}</p>
        </div>
      )}
    </QueryState>
  )
}
