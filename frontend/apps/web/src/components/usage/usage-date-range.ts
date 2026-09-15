import type { UsageWindow } from '@omnara/react'
import { format, startOfDay } from 'date-fns'

export interface UsageDateRange {
  from?: Date
  to?: Date
  window: UsageWindow
}

export const allTimeUsageRange: UsageDateRange = { window: {} }

export function usageDateRange(from?: Date, to?: Date): UsageDateRange {
  const since = from ? startOfDay(from) : undefined
  const until = to ? startOfDay(to) : undefined
  until?.setDate(until.getDate() + 1)
  return {
    from: since,
    to: to ? startOfDay(to) : undefined,
    window: { since: since?.toISOString(), until: until?.toISOString() },
  }
}

export function usageDateRangeLabel(range: UsageDateRange) {
  if (!range.from) return 'All time'
  const from = format(range.from, 'MMM d, yyyy')
  if (!range.to) return `From ${from}`
  return `${from} – ${format(range.to, 'MMM d, yyyy')}`
}

export function usageWindowIsActive(window: UsageWindow) {
  return window.since !== undefined || window.until !== undefined
}
