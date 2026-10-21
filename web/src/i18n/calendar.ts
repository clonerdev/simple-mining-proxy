/**
 * BCP 47 locale per language for numbers and dates. Persian keeps Latin
 * digits and the Gregorian calendar: hashrates, ports, addresses and the
 * timestamps in Docker logs are all Latin and Gregorian, and the panel
 * should read the same as the logs.
 */
const locales: Record<string, string> = { en: 'en-GB', ru: 'ru-RU', fa: 'fa-IR-u-ca-gregory-nu-latn' }
const fallback = 'en-GB'

/** The calendar dates are shown in. Only Persian offers a choice; Gregorian is the default. */
export const calendars = ['gregory', 'persian'] as const
export type Calendar = (typeof calendars)[number]

/**
 * The locale for a language and a calendar. The Jalali calendar is an option
 * for Persian only, and it keeps Latin digits like the rest of the panel.
 */
export function localeFor(lng: string, calendar: Calendar): string {
  return lng === 'fa' && calendar === 'persian' ? 'fa-IR-u-ca-persian-nu-latn' : (locales[lng] ?? fallback)
}
