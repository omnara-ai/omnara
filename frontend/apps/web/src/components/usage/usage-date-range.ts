import type { UsageWindow } from '@omnara/react'
import { format, startOfDay, subDays } from 'date-fns'

export interface UsageDateRange {
  from?: Date
  to?: Date
  /** Set when the range is the last `days` days, ending now. */
  days?: number
  window: UsageWindow
}

export const allTimeUsageRange: UsageDateRange = { window: {} }

export const defaultUsageDays = 30

export const usageRangePresetDays = [7, defaultUsageDays, 90] as const

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

export function lastDaysUsageRange(days: number): UsageDateRange {
  return { ...usageDateRange(subDays(new Date(), days - 1)), days }
}

export function defaultUsageRange() {
  return lastDaysUsageRange(defaultUsageDays)
}

export function isDefaultUsageRange(range: UsageDateRange) {
  return range.days === defaultUsageDays
}

export function lastDaysUsageWindow(days: number) {
  return lastDaysUsageRange(days).window
}

export function usageDateRangeLabel(range: UsageDateRange) {
  if (range.days !== undefined) return `Last ${range.days} days`
  if (!range.from) return 'All time'
  const from = format(range.from, 'MMM d, yyyy')
  if (!range.to) return `From ${from}`
  return `${from} – ${format(range.to, 'MMM d, yyyy')}`
}
