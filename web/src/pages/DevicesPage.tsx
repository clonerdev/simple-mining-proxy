import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { useCan, useCustomers, useDevices } from '../api/queries'
import type { Customer, Device, DeviceAction, DeviceStatus } from '../api/types'
import { QueryState, StatCard } from '../components/Panel'
import { useConfirm } from '../context/feedback'
import { CustomerDialog } from '../features/devices/CustomerDialog'
import { ConsentDialog, CustomerStatusDialog, KeyDialog } from '../features/devices/CustomerDialogs'
import { CustomersTable } from '../features/devices/CustomersTable'
import { DeviceActionDialog } from '../features/devices/DeviceActionDialog'
import { DeviceEditDialog } from '../features/devices/DeviceEditDialog'
import { DevicesTable } from '../features/devices/DevicesTable'
import { useRun } from '../features/devices/useRun'
import { useSessionState } from '../hooks/useSessionState'
import { deviceLabel, statusOrder } from '../lib/deviceActions'

type Filter = DeviceStatus | 'all'

/** What is open on top of the page: at most one dialog. */
type Dialog =
  | { kind: 'action'; device: Device; action: DeviceAction }
  | { kind: 'device'; device: Device }
  | { kind: 'customer'; customer: Customer | null }
  | { kind: 'state'; customer: Customer }
  | { kind: 'consent'; customer: Customer }
  | { kind: 'key'; customer: Customer; secret: string }

const rank = (s: DeviceStatus) => statusOrder.indexOf(s)

function matches(d: Device, q: string): boolean {
  if (!q) return true
  const hay = `${d.worker} ${d.customer_slug} ${d.customer_name} ${d.last_ip ?? ''} ${d.notes ?? ''}`.toLowerCase()
  return hay.includes(q.toLowerCase())
}

export function DevicesPage() {
  const { t } = useTranslation()
  const customers = useCustomers()
  const devices = useDevices()
  const canManage = useCan('devices.manage')
  const confirm = useConfirm()
  const { run } = useRun()
  const [dialog, setDialog] = useState<Dialog | null>(null)
  const [filter, setFilter] = useSessionState<Filter>('devices.filter', 'all')
  const [query, setQuery] = useState('')

  const all = useMemo(
    () => [...(devices.data?.devices ?? [])].sort((a, b) => rank(a.status) - rank(b.status) || deviceLabel(a).localeCompare(deviceLabel(b))),
    [devices.data],
  )
  const pending = all.filter((d) => d.status === 'pending')
  const shown = all.filter((d) => (filter === 'all' || d.status === filter) && matches(d, query))
  const counts = devices.data?.counts.devices ?? {}
  const close = () => setDialog(null)

  const onAction = (device: Device, action: DeviceAction) => setDialog({ kind: 'action', device, action })

  const deleteDevice = async (d: Device) => {
    const ok = await confirm({
      title: t('devices.deleteDeviceTitle', { device: deviceLabel(d) }),
      body: <p>{t('devices.deleteDeviceBody')}</p>,
      confirmLabel: t('devices.delete'),
      danger: true,
    })
    if (ok) await run(() => api.deleteDevice(d.id), { done: t('devices.deleted') })
  }

  const newKey = async (c: Customer) => {
    const ok = await confirm({
      title: t('devices.newKeyTitle', { name: c.name }),
      body: <p>{t('devices.newKeyBody')}</p>,
      confirmLabel: t('devices.newKey'),
      danger: true,
    })
    if (!ok) return
    let secret = ''
    const done = await run(async () => {
      secret = (await api.rotateCustomerKey(c.id)).key
    })
    if (done) setDialog({ kind: 'key', customer: c, secret })
  }

  const deleteCustomer = async (c: Customer) => {
    const ok = await confirm({
      title: t('devices.deleteCustomerTitle', { name: c.name }),
      body: <p>{t('devices.deleteCustomerBody')}</p>,
      confirmLabel: t('devices.delete'),
      danger: true,
    })
    if (ok) await run(() => api.deleteCustomer(c.id), { done: t('devices.deleted') })
  }

  const filters: Filter[] = ['all', ...statusOrder]

  return (
    <>
      <h2>{t('devices.title')}</h2>

      {customers.data && !customers.data.control && <div className="warnbox">{t('devices.controlOff')}</div>}

      <div className="cards">
        <StatCard label={t('devices.status.pending')} value={counts.pending ?? 0} sub={t('devices.cardPending')} />
        <StatCard label={t('devices.status.approved')} value={counts.approved ?? 0} sub={t('devices.cardApproved')} />
        <StatCard
          label={t('devices.cardStopped')}
          value={(counts.suspended ?? 0) + (counts.blocked ?? 0) + (counts.revoked ?? 0)}
          sub={t('devices.cardStoppedSub')}
        />
        <StatCard label={t('devices.customers')} value={customers.data?.customers.length ?? 0} sub="" />
      </div>

      <h3>{t('devices.pendingTitle', { n: pending.length })}</h3>
      <p className="small muted">{t('devices.pendingHelp')}</p>
      <QueryState error={devices.error} pending={devices.isPending}>
        {pending.length ? (
          <DevicesTable
            devices={pending}
            canManage={canManage}
            onAction={onAction}
            onEdit={(d) => setDialog({ kind: 'device', device: d })}
            onDelete={(d) => void deleteDevice(d)}
          />
        ) : (
          <div className="panel muted">{t('devices.pendingNone')}</div>
        )}
      </QueryState>

      <h3>{t('devices.allTitle')}</h3>
      <div className="toolbar">
        <div className="chips" role="group" aria-label={t('devices.state')}>
          {filters.map((f) => (
            <button
              key={f}
              type="button"
              className={f === filter ? 'chip active' : 'chip'}
              aria-pressed={f === filter}
              onClick={() => setFilter(f)}
            >
              {f === 'all' ? t('devices.filterAll') : t(`devices.status.${f}`)}
              {f !== 'all' && counts[f] ? ` ${counts[f]}` : ''}
            </button>
          ))}
        </div>
        <input
          type="search"
          className="filter"
          placeholder={t('devices.search')}
          aria-label={t('devices.search')}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
      </div>
      <QueryState error={devices.error} pending={devices.isPending}>
        {shown.length ? (
          <DevicesTable
            devices={shown}
            canManage={canManage}
            onAction={onAction}
            onEdit={(d) => setDialog({ kind: 'device', device: d })}
            onDelete={(d) => void deleteDevice(d)}
          />
        ) : (
          <div className="panel muted">{t('devices.none')}</div>
        )}
      </QueryState>

      <div className="split section-head">
        <h3>{t('devices.customers')}</h3>
        {canManage && (
          <button type="button" className="primary" onClick={() => setDialog({ kind: 'customer', customer: null })}>
            {t('devices.newCustomer')}
          </button>
        )}
      </div>
      <QueryState error={customers.error} pending={customers.isPending}>
        {customers.data?.customers.length ? (
          <CustomersTable
            customers={customers.data.customers}
            canManage={canManage}
            onEdit={(c) => setDialog({ kind: 'customer', customer: c })}
            onState={(c) => setDialog({ kind: 'state', customer: c })}
            onConsent={(c) => setDialog({ kind: 'consent', customer: c })}
            onKey={(c) => void newKey(c)}
            onDelete={(c) => void deleteCustomer(c)}
          />
        ) : (
          <div className="panel muted">{t('devices.noCustomers')}</div>
        )}
      </QueryState>

      {dialog?.kind === 'action' && <DeviceActionDialog device={dialog.device} action={dialog.action} onClose={close} />}
      {dialog?.kind === 'device' && <DeviceEditDialog device={dialog.device} onClose={close} />}
      {dialog?.kind === 'customer' && (
        <CustomerDialog
          customer={dialog.customer}
          languages={customers.data?.languages ?? []}
          onClose={close}
          onCreated={(customer, secret) => setDialog({ kind: 'key', customer, secret })}
        />
      )}
      {dialog?.kind === 'state' && <CustomerStatusDialog customer={dialog.customer} onClose={close} />}
      {dialog?.kind === 'consent' && <ConsentDialog customer={dialog.customer} onClose={close} />}
      {dialog?.kind === 'key' && <KeyDialog customer={dialog.customer} secret={dialog.secret} onClose={close} />}
    </>
  )
}
