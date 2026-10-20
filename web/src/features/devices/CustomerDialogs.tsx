import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { Customer, CustomerStatus } from '../../api/types'
import { CopyButton } from '../../components/CopyButton'
import { Modal } from '../../components/Modal'
import { locale } from '../../i18n'
import { formatDateTime } from '../../lib/format'
import { Field } from './Field'
import { useRun } from './useRun'

/** The connection key of a customer, shown once: only its hash is kept. */
export function KeyDialog({ customer, secret, onClose }: { customer: Customer; secret: string; onClose: () => void }) {
  const { t } = useTranslation()
  return (
    <Modal
      title={t('devices.keyTitle', { name: customer.name })}
      onClose={onClose}
      footer={
        <button type="button" className="primary" onClick={onClose}>
          {t('common.close')}
        </button>
      }
    >
      <p>{t('devices.keyHelp')}</p>
      <div className="split">
        <code className="mono key">{secret}</code>
        <CopyButton text={secret} />
      </div>
      <p className="small muted">{t('devices.keyLogin', { slug: customer.slug })}</p>
      <div className="warnbox">{t('devices.keyOnce')}</div>
    </Modal>
  )
}

const statuses: CustomerStatus[] = ['active', 'suspended', 'blocked']

/** Enables, disables or blocks a customer as a whole: all of the devices follow. */
export function CustomerStatusDialog({ customer, onClose }: { customer: Customer; onClose: () => void }) {
  const { t } = useTranslation()
  const { busy, run } = useRun()
  const [status, setStatus] = useState<CustomerStatus>(customer.status)
  const [reason, setReason] = useState('')
  const [error, setError] = useState('')

  const save = async () => {
    setError('')
    const ok = await run(() => api.setCustomerStatus(customer.id, status, reason.trim()), {
      done: t('devices.saved'),
      onFields: (f) => setError(Object.values(f).join(' ')),
    })
    if (ok) onClose()
  }

  return (
    <Modal
      title={t('devices.customerStateTitle', { name: customer.name })}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </button>
          <button
            type="button"
            className={status === 'active' ? 'primary' : 'primary danger'}
            onClick={() => void save()}
            disabled={busy || status === customer.status}
          >
            {t('common.save')}
          </button>
        </>
      }
    >
      <p className="small muted">{t('devices.customerStateHelp')}</p>
      <div className="form">
        <Field label={t('devices.state')}>
          <div className="chips" role="group" aria-label={t('devices.state')}>
            {statuses.map((s) => (
              <button
                key={s}
                type="button"
                className={s === status ? 'chip active' : 'chip'}
                aria-pressed={s === status}
                onClick={() => setStatus(s)}
              >
                {t(`devices.customerStatus.${s}`)}
              </button>
            ))}
          </div>
        </Field>
        <Field label={t('devices.reason')} error={error}>
          <input type="text" value={reason} maxLength={200} onChange={(e) => setReason(e.target.value)} />
        </Field>
      </div>
    </Modal>
  )
}

/**
 * The customer's agreement to the operator's share of their mining. Customers
 * have no access to the panel, so the administrator who holds the agreement
 * records it here, and the record names who did. Without it nothing is ever
 * taken from the customer's mining: the proxy checks it on every share.
 */
export function ConsentDialog({ customer, onClose }: { customer: Customer; onClose: () => void }) {
  const { t } = useTranslation()
  const loc = locale()
  const { busy, run } = useRun()
  const given = customer.fee_consent
  const [terms, setTerms] = useState(given?.terms ?? '')
  const [note, setNote] = useState(given?.note ?? '')
  const [errors, setErrors] = useState<Record<string, string>>({})

  const send = async (grant: boolean) => {
    setErrors({})
    const ok = await run(() => api.setFeeConsent(customer.id, grant, terms.trim(), note.trim()), {
      done: t('devices.saved'),
      onFields: setErrors,
    })
    if (ok) onClose()
  }

  return (
    <Modal
      title={t('devices.consentTitle', { name: customer.name })}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </button>
          {given && (
            <button type="button" className="danger" onClick={() => void send(false)} disabled={busy}>
              {t('devices.consentWithdraw')}
            </button>
          )}
          <button type="button" className="primary" onClick={() => void send(true)} disabled={busy}>
            {given ? t('devices.consentUpdate') : t('devices.consentGrant')}
          </button>
        </>
      }
    >
      <p className="small muted">{t('devices.consentHelp')}</p>
      <p>
        {given
          ? t('devices.consentGiven', { date: formatDateTime(given.at, loc), by: given.by })
          : t('devices.consentNone')}
      </p>
      <div className="form">
        <Field label={t('devices.consentTerms')} error={errors.terms}>
          <input type="text" value={terms} maxLength={200} onChange={(e) => setTerms(e.target.value)} />
        </Field>
        <Field label={t('devices.consentNote')} error={errors.note} help={t('devices.consentNoteHelp')}>
          <textarea rows={2} value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} />
        </Field>
      </div>
    </Modal>
  )
}
