import type { Device, DeviceAction, DeviceStatus } from '../api/types'

// What the administrator may ask for, state by state. It mirrors the
// transitions of internal/devices/model.go and only decides what to offer: the
// server checks every request again.
//
// In the words of the panel: suspend is "disable" and resume "enable"; block
// cuts a device off and unblock ("allow") restores the state it had before.
const offered: Record<DeviceStatus, DeviceAction[]> = {
  pending: ['approve', 'reject', 'block'],
  approved: ['suspend', 'block', 'revoke'],
  rejected: ['reopen', 'block'],
  suspended: ['resume', 'block', 'revoke'],
  blocked: ['unblock', 'revoke'],
  revoked: [],
}

export const actionsFor = (status: DeviceStatus): DeviceAction[] => offered[status]

/** Approve and resume are the actions that can set how long the device works. */
export const takesExpiry = (action: DeviceAction): boolean => action === 'approve' || action === 'resume'

/** Actions that cut a device off or close its case: they ask for confirmation and look dangerous. */
export const isDanger = (action: DeviceAction): boolean =>
  action === 'block' || action === 'revoke' || action === 'reject'

/** A record that is no longer in use can be deleted; the server decides again. */
export const canDelete = (status: DeviceStatus): boolean => status === 'revoked' || status === 'rejected'

/** Where devices are listed first: those waiting for a decision, then the working ones. */
export const statusOrder: DeviceStatus[] = ['pending', 'approved', 'suspended', 'blocked', 'rejected', 'revoked']

/** The name an administrator knows a device by: the customer's login name, then the worker. */
export const deviceLabel = (d: Pick<Device, 'customer_slug' | 'worker'>): string => `${d.customer_slug}.${d.worker}`

/** A whole number from a text input; null when it is empty, not a number or negative. */
export function parseCount(text: string): number | null {
  if (!/^\d{1,6}$/.test(text.trim())) return null
  return Number(text.trim())
}
