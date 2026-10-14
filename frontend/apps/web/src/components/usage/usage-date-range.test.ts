import { describe, expect, it } from 'vitest'

import {
  allTimeUsageRange,
  defaultUsageRange,
  isDefaultUsageRange,
  lastDaysUsageRange,
  lastHoursUsageRange,
  usageDateRange,
  usageDateRangeLabel,
} from '@/components/usage/usage-date-range'

describe('usageDateRange', () => {
  it('leaves all time unbounded', () => {
    expect(allTimeUsageRange.window).toEqual({})
    expect(usageDateRangeLabel(allTimeUsageRange)).toBe('All time')
  })

  it('covers whole local days, ending at the start of the day after the end date', () => {
    const range = usageDateRange(new Date(2026, 8, 1, 15), new Date(2026, 8, 3, 9))
    expect(range.window.since).toBe(new Date(2026, 8, 1).toISOString())
    expect(range.window.until).toBe(new Date(2026, 8, 4).toISOString())
    expect(usageDateRangeLabel(range)).toBe('Sep 1, 2026 – Sep 3, 2026')
  })

  it('leaves a missing end open-ended', () => {
    const range = usageDateRange(new Date(2026, 8, 1))
    expect(range.window.until).toBeUndefined()
    expect(usageDateRangeLabel(range)).toBe('From Sep 1, 2026')
  })

  it('defaults to the last 30 days, starting at local midnight and ending now', () => {
    const range = defaultUsageRange()
    const since = new Date()
    since.setHours(0, 0, 0, 0)
    since.setDate(since.getDate() - 29)
    expect(range.window).toEqual({ since: since.toISOString(), until: undefined })
    expect(isDefaultUsageRange(range)).toBe(true)
    expect(usageDateRangeLabel(range)).toBe('Last 30 days')
    expect(isDefaultUsageRange(lastDaysUsageRange(7))).toBe(false)
  })

  it('covers the last 24 hours in hourly buckets, starting on the hour', () => {
    const range = lastHoursUsageRange(24)
    const since = new Date()
    since.setMinutes(0, 0, 0)
    since.setHours(since.getHours() - 23)
    expect(range.window).toEqual({ since: since.toISOString() })
    expect(range.interval).toBe('hour')
    expect(isDefaultUsageRange(range)).toBe(false)
    expect(usageDateRangeLabel(range)).toBe('Last 24 hours')
  })
})
