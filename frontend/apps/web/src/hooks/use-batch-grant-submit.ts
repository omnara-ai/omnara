import { type SyntheticEvent, useState } from 'react'

import { collectBatchGrantFailures } from '@/lib/grant-failures'
import { idle, statusError, submitError, type SubmitStatus, submitting } from '@/lib/submit-status'

/**
 * Submit flow shared by the grant dialogs: grant every selected item, report
 * successes through onGranted as soon as they land, keep only the failed
 * items selected on partial failure, and close on full success.
 */
export function useBatchGrantSubmit<TItem>({
  label,
  fallbackError,
  itemKey,
  grant,
  onGranted,
  onSuccess,
}: {
  label: string
  fallbackError: string
  itemKey: (item: TItem) => string
  grant: (item: TItem) => Promise<unknown>
  onGranted?: (granted: TItem[]) => void
  onSuccess: () => void
}) {
  const [items, setItems] = useState<TItem[]>([])
  const [status, setStatus] = useState<SubmitStatus>(idle)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setStatus(submitting)
    try {
      const results = await Promise.allSettled(items.map((item) => grant(item)))
      const failures = collectBatchGrantFailures(items.map(itemKey), results, label)
      const failedIds = new Set(failures?.failedIds)
      const granted = items.filter((item) => !failedIds.has(itemKey(item)))
      if (granted.length > 0) onGranted?.(granted)
      if (failures) {
        setItems(items.filter((item) => failedIds.has(itemKey(item))))
        setStatus({ phase: 'error', message: failures.message })
        return
      }
      setItems([])
      setStatus(idle)
      onSuccess()
    } catch (err) {
      setStatus(submitError(err, fallbackError))
    }
  }

  return {
    items,
    setItems,
    isSubmitting: status.phase === 'submitting',
    errorMessage: statusError(status),
    submit,
  }
}
