import type { UsageWindow } from '@omnara/react'
import type { UsageTimeseriesInterval } from '@omnara/sdk'
import { format, startOfHour, subDays, subHours } from 'date-fns'

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

function utcDayStart(date: Date, offset = 0) {
  return new Date(
    Date.UTC(date.getFullYear(), date.getMonth(), date.getDate() + offset),
  ).toISOString()
}

/** Whole UTC days from the calendar day of `from` through that of `to`. */
export function usageDateRange(from?: Date, to?: Date): UsageDateRange {
  return { from, to, window: { since: from && utcDayStart(from), until: to && utcDayStart(to, 1) } }
}

export function lastDaysUsageRange(days: number): UsageDateRange {
  const now = new Date()
  const today = new Date(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate())
  return { ...usageDateRange(subDays(today, days - 1)), days }
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
