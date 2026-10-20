import { useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { useAudit } from '../api/queries'
import type { AuditResult } from '../api/types'
import { QueryState } from '../components/Panel'
import { AuditTable } from '../features/audit/AuditTable'
import {
  actionGroups,
  csvRows,
  emptyFilter,
  groupKey,
  ranges,
  results,
  toQuery,
  type AuditFilter,
  type RangeKey,
} from '../lib/audit'

/** The audit log: who did what in the panel, with filters and a CSV export. */
export function AuditPage() {
  const { t } = useTranslation()
  // What is typed, and what the list was asked for. Typed text is sent when the
  // form is submitted, not on every key; the choices below are sent at once.
  // The time of the request is kept, so that "the last 7 days" does not move
  // with every render and ask again.
  const [draft, setDraft] = useState<AuditFilter>(emptyFilter)
  const [applied, setApplied] = useState(() => ({ filter: emptyFilter, at: new Date() }))
  const query = useMemo(() => toQuery(applied.filter, applied.at), [applied])
  const audit = useAudit(query)
  const entries = useMemo(() => audit.data?.pages.flat() ?? [], [audit.data])

  const apply = (next: AuditFilter) => {
    setDraft(next)
    setApplied({ filter: next, at: new Date() })
  }
  const choose = (change: Partial<AuditFilter>) => apply({ ...draft, ...change })
  const submit = (e: FormEvent) => {
    e.preventDefault()
    apply(draft)
  }
  const refresh = () => setApplied((a) => ({ filter: a.filter, at: new Date() }))
  const download = () => window.location.assign(api.auditCsvUrl(query))

  return (
    <>
      <h2>{t('audit.title')}</h2>
      <p className="small muted">{t('audit.help')}</p>

      <div className="chips" role="group" aria-label={t('audit.range')}>
        {ranges.map((r: RangeKey) => (
          <button
            key={r}
            type="button"
            className={r === draft.range ? 'chip active' : 'chip'}
            aria-pressed={r === draft.range}
            onClick={() => choose({ range: r })}
          >
            {t(`audit.ranges.${r}`)}
          </button>
        ))}
      </div>

      <form className="toolbar" onSubmit={submit}>
        <select
          aria-label={t('audit.action')}
          value={draft.action}
          onChange={(e) => choose({ action: e.target.value })}
        >
          <option value="">{t('audit.groups.all')}</option>
          {actionGroups.map((g) => (
            <option key={g} value={g}>
              {t(`audit.groups.${groupKey(g)}`)}
            </option>
          ))}
        </select>
        <select
          aria-label={t('audit.result')}
          value={draft.result}
          onChange={(e) => choose({ result: e.target.value as AuditResult | '' })}
        >
          <option value="">{t('audit.results.all')}</option>
          {results.map((r) => (
            <option key={r} value={r}>
              {t(`audit.results.${r}`)}
            </option>
          ))}
        </select>
        <input
          type="text"
          aria-label={t('audit.actor')}
          placeholder={t('audit.actor')}
          title={t('audit.actorHelp')}
          value={draft.actor}
          onChange={(e) => setDraft({ ...draft, actor: e.target.value })}
        />
        <input
          type="search"
          className="filter"
          aria-label={t('audit.search')}
          placeholder={t('audit.search')}
          value={draft.text}
          onChange={(e) => setDraft({ ...draft, text: e.target.value })}
        />
        <button type="submit" className="primary">
          {t('audit.apply')}
        </button>
      </form>

      <div className="toolbar">
        <span className="small muted">{t('audit.shown', { n: entries.length })}</span>
        <button type="button" onClick={refresh}>
          {t('audit.refresh')}
        </button>
        <button type="button" onClick={download} title={t('audit.csvHelp', { n: csvRows })}>
          {t('audit.csv')}
        </button>
      </div>

      <QueryState error={audit.error} pending={audit.isPending}>
        {entries.length ? <AuditTable entries={entries} /> : <div className="panel muted">{t('audit.empty')}</div>}
        {audit.hasNextPage && (
          <div className="toolbar">
            <button type="button" onClick={() => void audit.fetchNextPage()} disabled={audit.isFetchingNextPage}>
              {audit.isFetchingNextPage ? t('common.loading') : t('audit.older')}
            </button>
          </div>
        )}
      </QueryState>
    </>
  )
}
