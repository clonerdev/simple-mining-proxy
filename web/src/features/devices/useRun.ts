import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError } from '../../api/client'
import { useRefreshDevices } from '../../api/queries'
import { useToast } from '../../context/feedback'
import { errorText, translateMsg } from '../../i18n/messages'

/**
 * Runs a change of a customer or a device: busy while it runs, fresh lists
 * after it, and on failure a toast, or the field messages when the caller
 * wants them next to its inputs.
 */
export function useRun() {
  const { i18n } = useTranslation()
  const toast = useToast()
  const refresh = useRefreshDevices()
  const [busy, setBusy] = useState(false)

  const run = async (
    change: () => Promise<unknown>,
    options: { done?: string; onFields?: (fields: Record<string, string>) => void } = {},
  ): Promise<boolean> => {
    setBusy(true)
    try {
      await change()
      refresh()
      if (options.done) toast(options.done)
      return true
    } catch (err) {
      if (err instanceof ApiError && options.onFields && Object.keys(err.fields).length) {
        options.onFields(Object.fromEntries(Object.entries(err.fields).map(([k, m]) => [k, translateMsg(i18n, m)])))
      } else {
        toast(errorText(i18n, err), 'error')
      }
      return false
    } finally {
      setBusy(false)
    }
  }

  return { busy, run }
}
