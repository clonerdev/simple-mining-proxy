import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { ShareKind, ShareRule } from '../../api/types'
import { Modal } from '../../components/Modal'
import { amountText, parseAmount, shareFraction, shareKinds, shareMax, validShare } from '../../lib/share'
import { formatPercent } from '../../lib/usage'
import { Field } from './Field'
import { useRun } from './useRun'

/** What the dialog sets the share of: a whole customer, or one device (which comes first). */
export type ShareSubject =
  | { kind: 'customer'; id: string; name: string; rule?: ShareRule; consent: boolean }
  | { kind: 'device'; id: string; name: string; rule?: ShareRule; customerRule?: ShareRule; consent: boolean }

/**
 * The size of the operator's share for one customer or device. It is only a
 * size: the customer's consent, the proxy-wide switch and the cap still decide
 * whether anything is taken, on every share.
 */
export function ShareDialog({ subject, onClose }: { subject: ShareSubject; onClose: () => void }) {
  const { t, i18n } = useTranslation()
  const { busy, run } = useRun()
  const own = subject.rule
  // Without a rule of its own the subject follows the wider one.
  const [useOwn, setUseOwn] = useState(own != null)
  const [kind, setKind] = useState<ShareKind>(own?.kind ?? 'percent')
  const [amount, setAmount] = useState(own ? amountText(own.amount) : '')
  const [error, setError] = useState('')

  const parsed = parseAmount(amount)
  const rule: ShareRule | null = parsed != null && validShare(kind, parsed) ? { kind, amount: parsed } : null
  const wider = subject.kind === 'device' ? subject.customerRule : undefined

  const save = async () => {
    setError('')
    if (!useOwn) {
      if (await run(() => (subject.kind === 'customer' ? api.setCustomerShare(subject.id, null) : api.setDeviceShare(subject.id, null)), { done: t('devices.saved') })) onClose()
      return
    }
    if (!rule) {
      setError(t('share.rules.invalid', { max: shareMax[kind] }))
      return
    }
    const ok = await run(() => (subject.kind === 'customer' ? api.setCustomerShare(subject.id, rule) : api.setDeviceShare(subject.id, rule)), {
      done: t('devices.saved'),
      onFields: (f) => setError(f.operator_share ?? Object.values(f)[0] ?? ''),
    })
    if (ok) onClose()
  }

  const inherit = subject.kind === 'device' ? (wider ? t('share.rules.inheritCustomer') : t('share.rules.inheritSetting')) : t('share.rules.inheritSetting')

  return (
    <Modal
      title={t('share.rules.title', { name: subject.name })}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </button>
          <button type="button" className="primary" onClick={() => void save()} disabled={busy}>
            {t('common.save')}
          </button>
        </>
      }
    >
      <p className="small muted">{t('share.rules.help')}</p>
      {!subject.consent && <div className="warnbox">{t('share.rules.noConsent')}</div>}
      <div className="form">
        <span className="form-label">{t('share.rules.size')}</span>
        <div className="form-field">
          <label>
            <input type="radio" name="share-scope" checked={!useOwn} onChange={() => setUseOwn(false)} /> {inherit}
          </label>
          <label>
            <input type="radio" name="share-scope" checked={useOwn} onChange={() => setUseOwn(true)} /> {t('share.rules.own')}
          </label>
        </div>
        {useOwn && (
          <>
            <Field label={t('share.rules.kind')}>
              <select value={kind} onChange={(e) => setKind(e.target.value as ShareKind)}>
                {shareKinds.map((k) => (
                  <option key={k} value={k}>
                    {t(`share.rules.kinds.${k}`)}
                  </option>
                ))}
              </select>
            </Field>
            <Field
              label={t('share.rules.amount')}
              error={error}
              help={rule ? (rule.amount === 0 ? t('share.rules.leftOut') : t('share.rules.about', { value: formatPercent(shareFraction(rule), i18n.language) })) : t('share.rules.range', { max: shareMax[kind] })}
            >
              <input type="text" inputMode="decimal" value={amount} autoComplete="off" onChange={(e) => setAmount(e.target.value)} />
            </Field>
          </>
        )}
      </div>
    </Modal>
  )
}
