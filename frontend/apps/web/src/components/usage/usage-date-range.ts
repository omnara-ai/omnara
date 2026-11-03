import type { UsageWindow } from '@omnara/react'
import type { UsageTimeseriesInterval } from '@omnara/sdk'
import { format, startOfDay, startOfHour, subDays, subHours } from 'date-fns'

export interface UsageDateRange {
  from?: Date
  to?: Date
  /** Set when the range is the last `days` days, ending now. */
  days?: number
  /** Set when the range is the last `hours` hours, ending now. */
  hours?: number
  window: UsageWindow
  /** Chart bucket width; omitted lets the API choose from the window length. */
  interval?: UsageTimeseriesInterval
}

export const allTimeUsageRange: UsageDateRange = { window: {} }

export const defaultUsageDays = 30

export const usageRangePresetDays = [7, defaultUsageDays, 90] as const

export const usageRangePresetHours = [24] as const

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

/** The last `hours` whole local hours, the current one included, in hourly buckets. */
export function lastHoursUsageRange(hours: number): UsageDateRange {
  const since = subHours(startOfHour(new Date()), hours - 1)
  return { hours, interval: 'hour', window: { since: since.toISOString() } }
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
  if (range.hours !== undefined) return `Last ${range.hours} hours`
  if (range.days !== undefined) return `Last ${range.days} days`
  if (!range.from) return 'All time'
  const from = format(range.from, 'MMM d, yyyy')
  if (!range.to) return `From ${from}`
  return `${from} – ${format(range.to, 'MMM d, yyyy')}`
}
