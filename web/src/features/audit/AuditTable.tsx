import { useTranslation } from 'react-i18next'
import type { AuditEntry } from '../../api/types'
import { locale } from '../../i18n'
import { resultClass, targetText } from '../../lib/audit'

/** Entries newest first: when, who, what, on what, and how it ended. */
export function AuditTable({ entries }: { entries: AuditEntry[] }) {
  const { t, i18n } = useTranslation()
  const loc = locale()
  return (
    <div className="panel table-wrap">
      <table className="responsive">
        <thead>
          <tr>
            <th>{t('audit.time')}</th>
            <th>{t('audit.who')}</th>
            <th>{t('audit.action')}</th>
            <th>{t('audit.target')}</th>
            <th>{t('audit.result')}</th>
            <th>{t('audit.message')}</th>
          </tr>
        </thead>
        <tbody>
          {entries.map((e) => {
            const typeKey = `audit.actorType.${e.actor.type}`
            const details = e.details && Object.keys(e.details).length ? JSON.stringify(e.details, null, 2) : ''
            return (
              <tr key={e.id}>
                <td className="primary small">
                  {new Date(e.time).toLocaleString(loc, { dateStyle: 'medium', timeStyle: 'medium' })}
                </td>
                <td data-label={t('audit.who')}>
                  <b>{e.actor.name || '—'}</b>
                  <div className="small muted">
                    {i18n.exists(typeKey) ? t(typeKey) : e.actor.type}
                    {e.actor.ip ? ` · ${e.actor.ip}` : ''}
                  </div>
                </td>
                <td className="mono small" data-label={t('audit.action')}>
                  {e.action}
                </td>
                <td className="small" data-label={t('audit.target')}>
                  {targetText(e.target) || <span className="muted">—</span>}
                </td>
                <td data-label={t('audit.result')}>
                  <span className={`badge ${resultClass(e.result)}`}>{t(`audit.results.${e.result}`)}</span>
                </td>
                <td className="small" data-label={t('audit.message')}>
                  {e.message}
                  {details && (
                    <details>
                      <summary>{t('audit.details')}</summary>
                      <pre className="audit-details mono">{details}</pre>
                    </details>
                  )}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
