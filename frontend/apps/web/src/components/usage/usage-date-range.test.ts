import { describe, expect, it } from 'vitest'

import {
  allTimeUsageRange,
  usageDateRange,
  usageDateRangeLabel,
  usageWindowIsActive,
} from '@/components/usage/usage-date-range'

describe('usageDateRange', () => {
  it('leaves all time unbounded', () => {
    expect(allTimeUsageRange.window).toEqual({})
    expect(usageWindowIsActive(allTimeUsageRange.window)).toBe(false)
    expect(usageDateRangeLabel(allTimeUsageRange)).toBe('All time')
  })

  it('covers whole local days, ending at the start of the day after the end date', () => {
    const range = usageDateRange(new Date(2026, 8, 1, 15), new Date(2026, 8, 3, 9))
    expect(range.window.since).toBe(new Date(2026, 8, 1).toISOString())
    expect(range.window.until).toBe(new Date(2026, 8, 4).toISOString())
    expect(usageWindowIsActive(range.window)).toBe(true)
    expect(usageDateRangeLabel(range)).toBe('Sep 1, 2026 – Sep 3, 2026')
  })

  it('leaves a missing end open-ended', () => {
    const range = usageDateRange(new Date(2026, 8, 1))
    expect(range.window.until).toBeUndefined()
    expect(usageDateRangeLabel(range)).toBe('From Sep 1, 2026')
  })
})
