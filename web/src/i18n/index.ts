import i18n from 'i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import { initReactI18next } from 'react-i18next'
import en from './locales/en.json'
import { isolateTechnicalRuns } from './bidi'
import { calendars, localeFor, type Calendar } from './calendar'
import fa from './locales/fa.json'
import ru from './locales/ru.json'

export { calendars, localeFor, type Calendar }
export const languages = ['en', 'ru', 'fa'] as const
export type Language = (typeof languages)[number]

/** Languages written right to left. Everything else is left to right. */
const rtlLanguages: ReadonlySet<Language> = new Set<Language>(['fa'])

/** Display name of each language in the language picker, in its own script. */
export const languageNames: Record<Language, string> = { en: 'English', ru: 'Русский', fa: 'فارسی' }

const storageKey = 'lang'
const calendarKey = 'calendar'

// The language is the one the operator picked (kept in localStorage) or the
// first supported one in the browser's preference list; English otherwise.
// Only an explicit choice is stored, so a browser setting keeps applying.
const bidiIslands = {
  type: 'postProcessor' as const,
  name: 'bidiIslands',
  process: (value: string) => (directionOf(currentLanguage()) === 'rtl' ? isolateTechnicalRuns(value) : value),
}

void i18n
  .use(bidiIslands)
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: { en: { translation: en }, ru: { translation: ru }, fa: { translation: fa } },
    supportedLngs: languages,
    nonExplicitSupportedLngs: true,
    load: 'languageOnly',
    fallbackLng: 'en',
    postProcess: ['bidiIslands'],
    detection: { order: ['localStorage', 'navigator'], lookupLocalStorage: storageKey, caches: [] },
    interpolation: { escapeValue: false },
  })

function asLanguage(lng: string | undefined): Language {
  return languages.find((l) => l === lng) ?? 'en'
}

/** Text direction of a language: the page layout follows it. */
export function directionOf(lng: Language): 'ltr' | 'rtl' {
  return rtlLanguages.has(lng) ? 'rtl' : 'ltr'
}

// <html lang dir> drives the hyphenation, the font and the whole layout
// direction (the stylesheet uses logical properties, so it mirrors by itself).
const syncDocument = (lng: string | undefined) => {
  const l = asLanguage(lng)
  document.documentElement.lang = l
  document.documentElement.dir = directionOf(l)
}
syncDocument(i18n.resolvedLanguage)
i18n.on('languageChanged', syncDocument)

export function currentLanguage(): Language {
  return asLanguage(i18n.resolvedLanguage)
}

export function setLanguage(lng: Language) {
  localStorage.setItem(storageKey, lng)
  void i18n.changeLanguage(lng)
}

/** The calendar the operator chose in this browser; Gregorian when nothing was chosen. */
export function currentCalendar(): Calendar {
  try {
    const v = localStorage.getItem(calendarKey)
    return calendars.find((c) => c === v) ?? 'gregory'
  } catch {
    return 'gregory'
  }
}

export function setCalendar(c: Calendar) {
  try {
    localStorage.setItem(calendarKey, c)
  } catch {
    // Storage may be blocked; the choice then lasts until the next load.
  }
  // Changing the language to the current one tells every screen to draw again.
  void i18n.changeLanguage(currentLanguage())
}

/** BCP 47 locale for numbers and dates in the current language and calendar. */
export function locale(): string {
  return localeFor(currentLanguage(), currentCalendar())
}

export default i18n
