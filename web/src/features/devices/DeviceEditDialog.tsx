import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { Device } from '../../api/types'
import { Modal } from '../../components/Modal'
import { locale } from '../../i18n'
import { deviceLabel, parseCount } from '../../lib/deviceActions'
import { formatDateTime } from '../../lib/format'
import { resolveExpiry } from '../../lib/expiry'
import { ExpiryPicker, type ExpiryChoice } from './ExpiryPicker'
import { Field } from './Field'
import { useRun } from './useRun'

/** Changes how long a device works, how many connections it may have and its notes; shows what is known of it. */
export function DeviceEditDialog({ device, onClose }: { device: Device; onClose: () => void }) {
  const { t } = useTranslation()
  const loc = locale()
  const { busy, run } = useRun()
  const [choice, setChoice] = useState<ExpiryChoice>({ preset: device.expires_at ? 'keep' : 'none', custom: '' })
  const [connections, setConnections] = useState(String(device.max_connections))
  const [notes, setNotes] = useState(device.notes ?? '')
  const [errors, setErrors] = useState<Record<string, string>>({})

  const save = async () => {
    const max = parseCount(connections)
    if (max === null) {
      setErrors({ max_connections: t('devices.countInvalid') })
      return
    }
    const r = resolveExpiry(choice.preset, choice.custom, new Date(), new Date())
    if (r.kind === 'error') {
      setErrors({ expires_at: t(`devices.expiry.error.${r.error}`) })
      return
    }
    setErrors({})
    const ok = await run(
      () =>
        api.updateDevice(device.id, {
          max_connections: max,
          notes: notes.trim(),
          // "keep" leaves the end out of the request; "no end" clears it with "".
          ...(r.kind === 'set' ? { expires_at: r.iso ?? '' } : {}),
        }),
      { done: t('devices.saved'), onFields: setErrors },
    )
    if (ok) onClose()
  }

  return (
    <Modal
      title={t('devices.editDeviceTitle', { device: deviceLabel(device) })}
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
      <div className="form">
        <Field label={t('devices.expiry.label')} error={errors.expires_at} help={t('devices.expiry.fromNow')}>
          <ExpiryPicker value={choice} onChange={setChoice} current={device.expires_at} />
        </Field>
        <Field label={t('devices.connections')} error={errors.max_connections} help={t('devices.connectionsHelp')}>
          <input type="text" inputMode="numeric" value={connections} onChange={(e) => setConnections(e.target.value)} />
        </Field>
        <Field label={t('devices.notes')} error={errors.notes}>
          <textarea rows={2} value={notes} maxLength={500} onChange={(e) => setNotes(e.target.value)} />
        </Field>
      </div>

      <h4>{t('devices.known')}</h4>
      <dl className="small facts">
        <dt>{t('devices.upstreamWorker')}</dt>
        <dd className="mono">{device.upstream_worker}</dd>
        <dt>{t('devices.firstSeen')}</dt>
        <dd>{formatDateTime(device.first_seen, loc)}</dd>
        <dt>{t('devices.lastSeen')}</dt>
        <dd>{formatDateTime(device.last_seen, loc)}</dd>
        <dt>{t('devices.lastIp')}</dt>
        <dd className="mono">{device.last_ip || '—'}</dd>
        <dt>{t('devices.userAgent')}</dt>
        <dd className="mono">{device.user_agent || '—'}</dd>
        {!!device.denied && (
          <>
            <dt>{t('devices.denied')}</dt>
            <dd>{device.denied}</dd>
          </>
        )}
      </dl>

      {!!device.history?.length && (
        <>
          <h4>{t('devices.history')}</h4>
          <ul className="small history">
            {[...device.history].reverse().map((c) => (
              <li key={`${c.at}-${c.to}`}>
                <span className="muted">{formatDateTime(c.at, loc)}</span> {t(`devices.status.${c.from}`)} →{' '}
                <b>{t(`devices.status.${c.to}`)}</b> · {c.by}
                {c.reason ? ` — ${c.reason}` : ''}
              </li>
            ))}
          </ul>
        </>
      )}
    </Modal>
  )
}
