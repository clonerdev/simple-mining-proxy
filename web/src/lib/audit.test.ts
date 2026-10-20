import { describe, expect, it } from 'vitest'
import { actionGroups, emptyFilter, groupKey, queryString, rangeStart, resultClass, ranges, targetText, toQuery } from './audit'

const now = new Date('2026-10-20T10:00:00.000Z')

describe('rangeStart', () => {
  it('counts back from now in whole seconds, in UTC', () => {
    expect(rangeStart('24h', now)).toBe('2026-10-19T10:00:00Z')
    expect(rangeStart('7d', now)).toBe('2026-10-13T10:00:00Z')
    expect(rangeStart('30d', now)).toBe('2026-09-20T10:00:00Z')
    expect(rangeStart('90d', now)).toBe('2026-07-22T10:00:00Z')
  })

  it('goes further back for each range', () => {
    const starts = ranges.map((r) => new Date(rangeStart(r, now)).getTime())
    expect([...starts].sort((a, b) => b - a)).toEqual(starts)
  })
})

describe('toQuery', () => {
  it('sends only the start when nothing else is chosen', () => {
    expect(toQuery(emptyFilter, now)).toEqual({ from: '2026-10-13T10:00:00Z' })
  })

  it('sends what is chosen and trims the typed text', () => {
    const q = toQuery({ range: '24h', action: 'device.', result: 'denied', actor: '  admin ', text: ' acme ' }, now)
    expect(q).toEqual({ from: '2026-10-19T10:00:00Z', action: 'device.', result: 'denied', actor: 'admin', q: 'acme' })
  })

  it('leaves out text that is only spaces', () => {
    expect(toQuery({ ...emptyFilter, actor: '   ', text: ' ' }, now)).toEqual({ from: '2026-10-13T10:00:00Z' })
  })
})

describe('queryString', () => {
  it('encodes the values and skips the empty ones', () => {
    expect(queryString({ from: '2026-10-19T10:00:00Z', q: 'a b&c', actor: '' })).toBe('from=2026-10-19T10%3A00%3A00Z&q=a+b%26c')
  })

  it('adds the extra parameters, which win over the query', () => {
    expect(queryString({ limit: 50 }, { limit: '200', before: '2026-10-19T09:00:00.5Z' })).toBe(
      'limit=200&before=2026-10-19T09%3A00%3A00.5Z',
    )
  })
})

describe('actionGroups', () => {
  it('are prefixes with a dot, so that "user." does not also take "users"', () => {
    for (const g of actionGroups) expect(g.endsWith('.'), g).toBe(true)
  })
})

describe('groupKey', () => {
  it('drops the dot that makes a group a prefix', () => {
    expect(groupKey('device.')).toBe('device')
    expect(actionGroups.map(groupKey).every((k) => !k.includes('.'))).toBe(true)
  })
})

describe('targetText', () => {
  it('names the kind and then the name, or the id when there is no name', () => {
    expect(targetText({ type: 'device', id: 'd1', name: 'acme.rack1' })).toBe('device: acme.rack1')
    expect(targetText({ type: 'customer', id: 'c1' })).toBe('customer: c1')
  })

  it('is empty when nothing is named, and a lone kind stands alone', () => {
    expect(targetText(undefined)).toBe('')
    expect(targetText({})).toBe('')
    expect(targetText({ type: 'audit' })).toBe('audit')
  })
})

describe('resultClass', () => {
  it('lets a refused or failed operation stand out', () => {
    expect(resultClass('ok')).toBe('ok')
    expect(resultClass('denied')).toBe('warn')
    expect(resultClass('failed')).toBe('bad')
  })
})
