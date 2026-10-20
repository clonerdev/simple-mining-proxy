import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { Device, DeviceAction } from '../../api/types'
import { Modal } from '../../components/Modal'
import { deviceLabel, isDanger, takesExpiry } from '../../lib/deviceActions'
import { resolveExpiry } from '../../lib/expiry'
import { ExpiryPicker, type ExpiryChoice } from './ExpiryPicker'
import { Field } from './Field'
import { useRun } from './useRun'

interface Props {
  device: Device
  action: DeviceAction
  onClose: () => void
}

/**
 * One change of a device's state. Approving or enabling a device also asks how
 * long it may work, so that it is never left working without the end that was
 * meant for it.
 */
export function DeviceActionDialog({ device, action, onClose }: Props) {
  const { t } = useTranslation()
  const { busy, run } = useRun()
  const expiring = takesExpiry(action)
  const [choice, setChoice] = useState<ExpiryChoice>({ preset: '1m', custom: '' })
  const [reason, setReason] = useState('')
  const [expiryError, setExpiryError] = useState('')
  const [reasonError, setReasonError] = useState('')
  const [formError, setFormError] = useState('')

  const submit = async () => {
    let end: string | null | undefined
    if (expiring) {
      const r = resolveExpiry(choice.preset, choice.custom, new Date(), new Date())
      if (r.kind === 'error') {
        setExpiryError(t(`devices.expiry.error.${r.error}`))
        return
      }
      end = r.kind === 'set' ? r.iso : undefined
    }
    setExpiryError('')
    setReasonError('')
    setFormError('')
    const ok = await run(
      async () => {
        await api.deviceAction(device.id, action, reason.trim(), end ?? undefined)
        // The action takes an end in the future but cannot remove one, so "no
        // end" on a device that still has one is a second, plain update.
        if (end === null && device.expires_at) await api.updateDevice(device.id, { expires_at: '' })
      },
      {
        done: t('devices.saved'),
        onFields: (f) => {
          setExpiryError(f.expires_at ?? '')
          setReasonError(f.reason ?? '')
          setFormError(
            Object.entries(f)
              .filter(([k]) => k !== 'expires_at' && k !== 'reason')
              .map(([, m]) => m)
              .join(' '),
          )
        },
      },
    )
    if (ok) onClose()
  }

  const label = t(`devices.action.${action}`)
  return (
    <Modal
      title={t('devices.actionTitle', { action: label, device: deviceLabel(device) })}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </button>
          <button type="button" className={isDanger(action) ? 'primary danger' : 'primary'} onClick={() => void submit()} disabled={busy}>
            {label}
          </button>
        </>
      }
    >
      <p className="small muted">{t(`devices.actionHelp.${action}`)}</p>
      <div className="form">
        {expiring && (
          <Field label={t('devices.expiry.label')} error={expiryError}>
            <ExpiryPicker value={choice} onChange={setChoice} />
          </Field>
        )}
        <Field label={t('devices.reason')} error={reasonError}>
          <input type="text" value={reason} maxLength={200} onChange={(e) => setReason(e.target.value)} />
        </Field>
      </div>
      {formError && <div className="field-error">{formError}</div>}
    </Modal>
  )
}
