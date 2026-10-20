import { describe, expect, it } from 'vitest'
import type { DeviceAction, DeviceStatus } from '../api/types'
import { actionsFor, canDelete, deviceLabel, isDanger, parseCount, statusOrder, takesExpiry } from './deviceActions'

const statuses: DeviceStatus[] = ['pending', 'approved', 'rejected', 'suspended', 'blocked', 'revoked']

describe('actionsFor', () => {
  it('offers the choices of an administrator for each state', () => {
    expect(actionsFor('pending')).toEqual(['approve', 'reject', 'block'])
    expect(actionsFor('approved')).toEqual(['suspend', 'block', 'revoke'])
    expect(actionsFor('suspended')).toEqual(['resume', 'block', 'revoke'])
    expect(actionsFor('blocked')).toEqual(['unblock', 'revoke'])
    expect(actionsFor('rejected')).toEqual(['reopen', 'block'])
  })

  it('offers nothing for a revoked device: only deleting its record is left', () => {
    expect(actionsFor('revoked')).toEqual([])
    expect(canDelete('revoked')).toBe(true)
  })

  it('never offers an action the state does not allow', () => {
    // The same table as transitions in internal/devices/model.go.
    const allowed: Record<DeviceAction, DeviceStatus[]> = {
      approve: ['pending'],
      reject: ['pending'],
      reopen: ['rejected'],
      suspend: ['approved'],
      resume: ['suspended'],
      block: ['pending', 'approved', 'rejected', 'suspended'],
      unblock: ['blocked'],
      revoke: ['approved', 'suspended', 'blocked'],
    }
    for (const s of statuses) for (const a of actionsFor(s)) expect(allowed[a], `${a} from ${s}`).toContain(s)
  })

  it('offers every action of the server from some state', () => {
    const all = new Set(statuses.flatMap(actionsFor))
    expect([...all].sort()).toEqual(
      ['approve', 'block', 'reject', 'reopen', 'resume', 'revoke', 'suspend', 'unblock'].sort(),
    )
  })
})

describe('takesExpiry', () => {
  it('is true only for approve and resume, the actions the server accepts an end with', () => {
    expect(takesExpiry('approve')).toBe(true)
    expect(takesExpiry('resume')).toBe(true)
    for (const a of ['reject', 'reopen', 'suspend', 'block', 'unblock', 'revoke'] as const) {
      expect(takesExpiry(a), a).toBe(false)
    }
  })
})

describe('isDanger', () => {
  it('marks the actions that cut a device off or close its case', () => {
    expect(['block', 'revoke', 'reject'].every((a) => isDanger(a as DeviceAction))).toBe(true)
    expect(['approve', 'resume', 'unblock', 'suspend', 'reopen'].some((a) => isDanger(a as DeviceAction))).toBe(false)
  })
})

describe('statusOrder', () => {
  it('lists every state once, the ones waiting for a decision first', () => {
    expect([...statusOrder].sort()).toEqual([...statuses].sort())
    expect(statusOrder[0]).toBe('pending')
  })
})

describe('deviceLabel', () => {
  it('names a device as customer.worker, the way its ASIC logs in', () => {
    expect(deviceLabel({ customer_slug: 'acme', worker: 'rack1.s21-01' })).toBe('acme.rack1.s21-01')
  })
})

describe('parseCount', () => {
  it('reads a whole number', () => {
    expect(parseCount('0')).toBe(0)
    expect(parseCount(' 12 ')).toBe(12)
  })

  it('refuses anything else, so that "5 devices" or "-1" is not sent as a number', () => {
    for (const bad of ['', ' ', '-1', '1.5', '5 devices', '1e3', '1234567']) expect(parseCount(bad), bad).toBeNull()
  })
})
