import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { Customer, CustomerInput } from '../../api/types'
import { Modal } from '../../components/Modal'
import { parseCount } from '../../lib/deviceActions'
import { resolveExpiry } from '../../lib/expiry'
import { ExpiryPicker, type ExpiryChoice } from './ExpiryPicker'
import { Field } from './Field'
import { useRun } from './useRun'

interface Props {
  /** null makes a new customer. */
  customer: Customer | null
  languages: string[]
  onClose: () => void
  /** The connection key of a new customer, shown once. */
  onCreated: (customer: Customer, key: string) => void
}

/** Create or edit a customer: the name, the pool account, the limits, how long the customer works. */
export function CustomerDialog({ customer, languages, onClose, onCreated }: Props) {
  const { t } = useTranslation()
  const { busy, run } = useRun()
  const [name, setName] = useState(customer?.name ?? '')
  const [slug, setSlug] = useState(customer?.slug ?? '')
  const [account, setAccount] = useState(customer?.account ?? '')
  const [maxDevices, setMaxDevices] = useState(String(customer?.max_devices ?? 0))
  const [maxConnections, setMaxConnections] = useState(String(customer?.max_connections ?? 0))
  const [choice, setChoice] = useState<ExpiryChoice>({ preset: customer?.expires_at ? 'keep' : 'none', custom: '' })
  const [language, setLanguage] = useState(customer?.language ?? '')
  const [notes, setNotes] = useState(customer?.notes ?? '')
  const [errors, setErrors] = useState<Record<string, string>>({})

  const save = async () => {
    const devices = parseCount(maxDevices)
    const connections = parseCount(maxConnections)
    const local: Record<string, string> = {}
    if (devices === null) local.max_devices = t('devices.countInvalid')
    if (connections === null) local.max_connections = t('devices.countInvalid')
    const r = resolveExpiry(choice.preset, choice.custom, new Date(), new Date())
    if (r.kind === 'error') local.expires_at = t(`devices.expiry.error.${r.error}`)
    if (Object.keys(local).length || devices === null || connections === null || r.kind === 'error') {
      setErrors(local)
      return
    }
    setErrors({})
    const body: CustomerInput = {
      name: name.trim(),
      account: account.trim(),
      max_devices: devices,
      max_connections: connections,
      language,
      notes: notes.trim(),
    }
    // A new customer with "no end" simply has none; when editing, "" clears an end and "keep" leaves it.
    if (r.kind === 'set' && (r.iso || customer)) body.expires_at = r.iso ?? ''

    const made: { value?: { customer: Customer; key: string } } = {}
    const ok = await run(
      async () => {
        if (customer) await api.updateCustomer(customer.id, body)
        else made.value = await api.createCustomer({ ...body, slug: slug.trim().toLowerCase() })
      },
      { done: t('devices.saved'), onFields: setErrors },
    )
    if (!ok) return
    onClose()
    if (made.value) onCreated(made.value.customer, made.value.key)
  }

  return (
    <Modal
      title={customer ? t('devices.editCustomerTitle', { name: customer.name }) : t('devices.newCustomerTitle')}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </button>
          <button type="button" className="primary" onClick={() => void save()} disabled={busy}>
            {customer ? t('common.save') : t('devices.createCustomer')}
          </button>
        </>
      }
    >
      <div className="form">
        <Field label={t('devices.customerName')} error={errors.name}>
          <input type="text" value={name} maxLength={80} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label={t('devices.slug')} error={errors.slug} help={customer ? t('devices.slugFixed') : t('devices.slugHelp')}>
          <input
            type="text"
            className="mono"
            value={slug}
            maxLength={32}
            disabled={!!customer}
            autoCapitalize="none"
            onChange={(e) => setSlug(e.target.value)}
          />
        </Field>
        <Field label={t('devices.account')} error={errors.account} help={t('devices.accountHelp')}>
          <input type="text" className="mono" value={account} maxLength={64} onChange={(e) => setAccount(e.target.value)} />
        </Field>
        <Field label={t('devices.maxDevices')} error={errors.max_devices} help={t('devices.maxDevicesHelp')}>
          <input type="text" inputMode="numeric" value={maxDevices} onChange={(e) => setMaxDevices(e.target.value)} />
        </Field>
        <Field label={t('devices.maxConnections')} error={errors.max_connections} help={t('devices.maxConnectionsHelp')}>
          <input type="text" inputMode="numeric" value={maxConnections} onChange={(e) => setMaxConnections(e.target.value)} />
        </Field>
        <Field label={t('devices.customerEnds')} error={errors.expires_at} help={t('devices.expiry.fromNow')}>
          <ExpiryPicker value={choice} onChange={setChoice} current={customer?.expires_at} />
        </Field>
        <Field label={t('devices.language')} error={errors.language} help={t('devices.languageHelp')}>
          <select value={language} onChange={(e) => setLanguage(e.target.value)}>
            <option value="">—</option>
            {languages.map((l) => (
              <option key={l} value={l}>
                {l}
              </option>
            ))}
          </select>
        </Field>
        <Field label={t('devices.notes')} error={errors.notes}>
          <textarea rows={2} value={notes} maxLength={500} onChange={(e) => setNotes(e.target.value)} />
        </Field>
      </div>
    </Modal>
  )
}
