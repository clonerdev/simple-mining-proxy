import { useTranslation } from 'react-i18next'
import { calendars, currentCalendar, currentLanguage, languageNames, languages, setCalendar, setLanguage, type Calendar, type Language } from '../i18n'

export function LanguageSelect() {
  const { t } = useTranslation()
  return (
    <>
      <select
        className="lang"
        aria-label={t('nav.language')}
        value={currentLanguage()}
        onChange={(e) => setLanguage(e.target.value as Language)}
      >
        {languages.map((l) => (
          <option key={l} value={l} lang={l}>
            {languageNames[l]}
          </option>
        ))}
      </select>
      {/* The Jalali calendar is offered in Persian only; Gregorian stays the default, like the logs. */}
      {currentLanguage() === 'fa' && (
        <select className="lang" aria-label={t('nav.calendar')} value={currentCalendar()} onChange={(e) => setCalendar(e.target.value as Calendar)}>
          {calendars.map((c) => (
            <option key={c} value={c}>
              {t(`nav.calendars.${c}`)}
            </option>
          ))}
        </select>
      )}
    </>
  )
}
