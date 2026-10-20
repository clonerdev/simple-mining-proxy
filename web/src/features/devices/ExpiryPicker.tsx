import { useTranslation } from 'react-i18next'
import { locale } from '../../i18n'
import { presetEnd, presets, toLocalInput, type Preset } from '../../lib/expiry'

export interface ExpiryChoice {
  preset: Preset
  /** The value of the date input, when preset is "custom". */
  custom: string
}

interface Props {
  value: ExpiryChoice
  onChange: (choice: ExpiryChoice) => void
  /** Presets count from here; now when absent. */
  from?: Date
  /** Offers "keep as it is" when the thing being edited already has an end. */
  current?: string | null
  /** Said under the choices, for example that they count from the current end. */
  hint?: string
  error?: string
}

/** How long something may work: a preset such as one month, a date, or no end. */
export function ExpiryPicker({ value, onChange, from, current, hint, error }: Props) {
  const { t } = useTranslation()
  const loc = locale()
  const choices: Preset[] = current ? ['keep', ...presets] : [...presets]
  const show = (d: Date) => d.toLocaleString(loc, { dateStyle: 'medium', timeStyle: 'short' })

  let summary = ''
  if (value.preset === 'keep' && current) summary = t('devices.expiry.endsOn', { date: show(new Date(current)) })
  else if (value.preset === 'none') summary = t('devices.expiry.noEndHelp')
  else if (value.preset !== 'custom' && value.preset !== 'keep') {
    const end = presetEnd(value.preset, from ?? new Date())
    if (end) summary = t('devices.expiry.endsOn', { date: show(end) })
  }

  return (
    <div>
      <div className="chips" role="group" aria-label={t('devices.expiry.label')}>
        {choices.map((p) => (
          <button
            key={p}
            type="button"
            className={p === value.preset ? 'chip active' : 'chip'}
            aria-pressed={p === value.preset}
            onClick={() => onChange({ ...value, preset: p })}
          >
            {t(`devices.expiry.${p}`)}
          </button>
        ))}
      </div>
      {value.preset === 'custom' && (
        <input
          type="datetime-local"
          aria-label={t('devices.expiry.custom')}
          className={error ? 'invalid' : undefined}
          min={toLocalInput(new Date())}
          value={value.custom}
          onChange={(e) => onChange({ ...value, custom: e.target.value })}
        />
      )}
      {summary && <div className="small muted">{summary}</div>}
      {hint && <div className="small muted">{hint}</div>}
      {error && <div className="field-error">{error}</div>}
    </div>
  )
}
