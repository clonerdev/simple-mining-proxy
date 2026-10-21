import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { OperatorProbe } from '../../api/types'
import { Modal } from '../../components/Modal'
import { errorText } from '../../i18n/messages'
import { hostPort } from '../../lib/addresses'

/** Runs the second-worker probe once and shows each step per address, and what it does not prove. */
export function OperatorProbeDialog({ run, onClose }: { run: () => Promise<OperatorProbe>; onClose: () => void }) {
  const { t, i18n } = useTranslation()
  const [result, setResult] = useState<OperatorProbe | null>(null)
  const [error, setError] = useState<unknown>(null)
  const started = useRef(false)

  useEffect(() => {
    if (started.current) return // StrictMode runs effects twice in development
    started.current = true
    run().then(setResult, setError)
  }, [run])

  return (
    <Modal
      title={t('probe.title')}
      onClose={onClose}
      footer={
        <button type="button" className="primary" onClick={onClose}>
          {t('common.close')}
        </button>
      }
    >
      {!result && !error && <div className="muted">{t('probe.running')}</div>}
      {error != null && (
        <>
          <span className="badge bad">{t('test.failed')}</span>
          <p className="mono small wrap">{errorText(i18n, error)}</p>
        </>
      )}
      {result && (
        <>
          {result.addresses.map((a) => (
            <section key={hostPort(a)}>
              <p>
                <span className="mono">{hostPort(a)}</span>{' '}
                <span className={a.verdict === 'authorize_accepted' ? 'badge ok' : 'badge bad'}>{t(`probe.verdict.${a.verdict}`)}</span>
              </p>
              <ul className="plain">
                {a.steps.map((s) => (
                  <li key={s.id} className="test-row">
                    <span>{t(`probe.step.${s.id}`)}</span>
                    {s.skipped ? (
                      <span className="muted small">{t('probe.skipped')}</span>
                    ) : s.ok ? (
                      <span className="lvl-ok">{t('test.ok')}</span>
                    ) : (
                      <span className="lvl-error small wrap">{s.detail || t('test.failed')}</span>
                    )}
                  </li>
                ))}
              </ul>
              {a.verdict !== 'authorize_accepted' && <p className="small muted">{t(`probe.advice.${a.verdict}`)}</p>}
            </section>
          ))}
          {/* The probe sends no share: this holds for every result and is never left out. */}
          <div className="warnbox">
            <strong>{t('probe.notProvedTitle')}</strong> {t('probe.notProved')}
          </div>
          <ol className="small">
            <li>{t('probe.how1')}</li>
            <li>{t('probe.how2')}</li>
            <li>{t('probe.how3')}</li>
          </ol>
        </>
      )}
    </Modal>
  )
}
