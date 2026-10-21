import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { keys, useCan, useStatus } from '../api/queries'
import { href, routes, type Route } from '../hooks/useHashRoute'
import { formatDuration } from '../lib/format'
import { LanguageSelect } from './LanguageSelect'

export function Header({ route }: { route: Route }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const uptime = useStatus().data?.uptime_seconds
  // A screen is listed only for an account that may read it; the server checks again.
  const seesDevices = useCan('devices.view')
  const seesAudit = useCan('audit.view')
  const seesShare = useCan('billing.view')

  const logout = async () => {
    try {
      await api.logout()
    } finally {
      qc.removeQueries()
      qc.setQueryData(keys.me, null)
    }
  }

  return (
    <header className="top">
      <div className="brand">{t('app.brand')}</div>
      <nav aria-label={t('app.brand')}>
        {routes.filter((r) => (r !== 'devices' || seesDevices) && (r !== 'audit' || seesAudit) && (r !== 'share' || seesShare)).map((r) => (
          <a key={r} href={href(r)} className={r === route ? 'active' : undefined} aria-current={r === route ? 'page' : undefined}>
            {t(`nav.${r}`)}
          </a>
        ))}
      </nav>
      <div className="right">
        {uptime !== undefined && <span className="muted small uptime">{t('nav.uptime', { value: formatDuration(uptime, t) })}</span>}
        <LanguageSelect />
        <button type="button" className="link" onClick={() => void logout()}>
          {t('nav.logout')}
        </button>
      </div>
    </header>
  )
}
