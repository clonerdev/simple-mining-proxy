import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import en from './locales/en.json'
import fa from './locales/fa.json'
import ru from './locales/ru.json'

type Tree = { [key: string]: string | Tree }

const pluralSuffix = /_(zero|one|two|few|many|other)$/

function flatten(tree: Tree, prefix = ''): Map<string, string> {
  const out = new Map<string, string>()
  for (const [k, v] of Object.entries(tree)) {
    const key = prefix ? `${prefix}.${k}` : k
    if (typeof v === 'string') out.set(key, v)
    else for (const [k2, v2] of flatten(v, key)) out.set(k2, v2)
  }
  return out
}

const enKeys = flatten(en)
const translations = { ru: flatten(ru), fa: flatten(fa) }
const base = (keys: Map<string, string>) => new Set([...keys.keys()].map((k) => k.replace(pluralSuffix, '')))

const placeholders = (s: string) => [...s.matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort()

// Single-brace tokens such as {worker} are filled in by the server, not by
// i18next: they must survive translation untouched.
const serverTokens = (s: string) => [...s.matchAll(/(?<!\{)\{(\w+)\}(?!\})/g)].map((m) => m[1]).sort()

// The plural forms a language needs, per Intl.PluralRules.
const pluralForms = (lng: string) => new Intl.PluralRules(lng).resolvedOptions().pluralCategories

describe.each(Object.entries(translations))('locale %s', (lng, keys) => {
  it('has the same keys as English', () => {
    expect([...base(keys)].sort()).toEqual([...base(enKeys)].sort())
  })

  it('uses the same placeholders', () => {
    for (const [key, text] of enKeys) {
      const b = key.replace(pluralSuffix, '')
      const translated = keys.get(key) ?? keys.get(`${b}_other`) ?? keys.get(`${b}_many`) ?? ''
      expect(placeholders(translated), key).toEqual(placeholders(text))
      expect(serverTokens(translated), key).toEqual(serverTokens(text))
    }
  })

  it('has every plural form the language uses', () => {
    for (const key of enKeys.keys()) {
      if (!key.endsWith('_one')) continue
      const b = key.slice(0, -'_one'.length)
      for (const form of pluralForms(lng)) expect(keys.has(`${b}_${form}`), `${b}_${form}`).toBe(true)
    }
  })

  it('has no empty text', () => {
    for (const [key, text] of keys) expect(text.trim(), key).not.toBe('')
  })
})

// The server sends message keys (internal/apierr) and setting keys; every
// one of them needs a translation.
describe('server keys', () => {
  const root = join(import.meta.dirname, '../../../internal')
  const goFiles: string[] = []
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      const path = join(dir, name)
      if (statSync(path).isDirectory()) walk(path)
      else if (name.endsWith('.go') && !name.endsWith('_test.go')) goFiles.push(path)
    }
  }
  walk(root)
  const source = goFiles.map((f) => readFileSync(f, 'utf8')).join('\n')

  it('translate every error key', () => {
    const keys = new Set([...source.matchAll(/apierr\.M\("([a-z_]+)"/g)].map((m) => m[1]))
    expect(keys.size).toBeGreaterThan(20)
    for (const key of keys) {
      expect(enKeys.has(`errors.${key}`), `errors.${key}`).toBe(true)
    }
  })

  it('translate every account problem', () => {
    const source = readFileSync(join(root, 'accounts/messages.go'), 'utf8')
    const keys = [...source.matchAll(/^\t"([a-z_]+)":/gm)].map((m) => m[1])
    expect(keys.length).toBeGreaterThan(10)
    for (const key of keys) {
      expect(enKeys.has(`errors.account_${key}`), `errors.account_${key}`).toBe(true)
    }
  })

  it('translate every device problem', () => {
    const source = readFileSync(join(root, 'devices/messages.go'), 'utf8')
    const keys = [...source.matchAll(/^\t"([a-z_]+)":/gm)].map((m) => m[1])
    expect(keys.length).toBeGreaterThan(10)
    for (const key of keys) {
      expect(enKeys.has(`errors.device_${key}`), `errors.device_${key}`).toBe(true)
    }
  })

  it('describe every setting and group', () => {
    const registry = readFileSync(join(root, 'settings/registry.go'), 'utf8')
    const settingKeys = [...registry.matchAll(/Key: "([a-z_]+)"/g)].map((m) => m[1])
    expect(settingKeys.length).toBeGreaterThan(10)
    for (const key of settingKeys) {
      expect(enKeys.has(`settings.items.${key}.title`), key).toBe(true)
      expect(enKeys.has(`settings.items.${key}.desc`), key).toBe(true)
    }
    const groups = /var Groups = \[\]string\{([^}]*)\}/.exec(registry)?.[1] ?? ''
    for (const [, g] of groups.matchAll(/"([a-z_]+)"/g)) {
      expect(enKeys.has(`settings.groups.${g}`), g).toBe(true)
    }
  })
})
