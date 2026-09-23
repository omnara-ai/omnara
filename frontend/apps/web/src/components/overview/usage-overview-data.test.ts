import type { OrgOverviewUsageResponse, UsageTotals } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import {
  labelledColumns,
  noProfileSeriesKey,
  otherSeriesKey,
  usageChartData,
  usageMeasureValue,
  usageTicks,
  usageWindowSince,
} from '@/components/overview/usage-overview-data'

function totals(input: number, output: number, cost: string, calls: number): UsageTotals {
  return {
    model_calls: calls,
    tokens: {
      input_tokens_total: input,
      uncached_input_tokens: input,
      cache_read_input_tokens: 0,
      cache_write_input_tokens: 0,
      output_tokens_total: output,
      reasoning_output_tokens: 0,
    },
    cost: { provider_reported_usd: cost, model_calls_with_reported_cost: calls },
  }
}

const opus = 'mdl_aaaaaaaaaaaaaaaaaaaaaaaaaa'
const sonnet = 'mdl_bbbbbbbbbbbbbbbbbbbbbbbbbb'

const usage: OrgOverviewUsageResponse = {
  totals: totals(800, 90, '3.5', 10),
  previous_totals: totals(400, 100, '1', 4),
  active_agents: 3,
  groups: [
    { id: opus, name: 'Opus', totals: totals(500, 50, '2.5', 4) },
    { id: sonnet, name: 'Sonnet', totals: totals(300, 30, '0.75', 4) },
  ],
  intervals: [
    {
      start: '2026-09-21T07:00:00Z',
      totals: totals(500, 50, '2.5', 4),
      groups: [{ id: opus, totals: totals(500, 50, '2.5', 4) }],
    },
    { start: '2026-09-22T07:00:00Z', totals: totals(0, 0, '0', 0), groups: [] },
    {
      start: '2026-09-23T07:00:00Z',
      totals: totals(300, 40, '1', 6),
      groups: [{ id: sonnet, totals: totals(300, 30, '0.75', 4) }],
    },
  ],
}

describe('usageWindowSince', () => {
  it('starts at local midnight 29 days back so today is the last of 30 days', () => {
    expect(usageWindowSince(new Date(2026, 8, 23, 14, 37, 12))).toEqual(new Date(2026, 7, 25))
  })
})

describe('usageMeasureValue', () => {
  it('reads tokens as input plus output, cost as a number, and calls as counted', () => {
    const value = totals(120, 30, '0.0125', 3)
    expect(usageMeasureValue(value, 'tokens')).toBe(150)
    expect(usageMeasureValue(value, 'cost')).toBe(0.0125)
    expect(usageMeasureValue(value, 'calls')).toBe(3)
  })
})

describe('usageChartData', () => {
  it('keeps ranked groups, derives Other from the remainder, and stacks every interval', () => {
    const data = usageChartData(usage)
    expect(data.series.map((series) => [series.key, series.total])).toEqual([
      [opus, 550],
      [sonnet, 330],
      [otherSeriesKey, 10],
    ])
    expect(data.series[0]?.color).toBe('var(--chart-1)')
    expect(data.series[2]?.color).toBe('var(--muted-foreground)')
    expect(data.columns.map((column) => column.total)).toEqual([550, 0, 340])
    expect(Object.fromEntries(data.columns[0]?.values ?? [])).toEqual({ [opus]: 550 })
    expect(Object.fromEntries(data.columns[2]?.values ?? [])).toEqual({
      [sonnet]: 330,
      [otherSeriesKey]: 10,
    })
    expect(data.columns[1]?.values.size).toBe(0)
  })

  it('omits Other when the named groups cover everything', () => {
    const covered = usageChartData({ ...usage, totals: totals(800, 80, '3.25', 8) })
    expect(covered.series.map((series) => series.key)).toEqual([opus, sonnet])
  })

  it('names the group without an id as usage without a profile', () => {
    const profile = 'aprf_aaaaaaaaaaaaaaaaaaaaaaaaaa'
    const data = usageChartData({
      ...usage,
      totals: totals(150, 30, '0', 2),
      groups: [
        { id: profile, name: 'Reviewer', totals: totals(100, 20, '0', 1) },
        { totals: totals(50, 10, '0', 1) },
      ],
      intervals: [
        {
          start: '2026-09-23T07:00:00Z',
          totals: totals(150, 30, '0', 2),
          groups: [
            { id: profile, totals: totals(100, 20, '0', 1) },
            { totals: totals(50, 10, '0', 1) },
          ],
        },
      ],
    })
    expect(data.series.map((series) => [series.key, series.name, series.total])).toEqual([
      [profile, 'Reviewer', 120],
      [noProfileSeriesKey, 'No profile', 60],
    ])
    expect(Object.fromEntries(data.columns[0]?.values ?? [])).toEqual({
      [profile]: 120,
      [noProfileSeriesKey]: 60,
    })
  })
})

describe('usageTicks', () => {
  it('rounds the scale up to clean steps', () => {
    expect(usageTicks(0)).toEqual([0])
    expect(usageTicks(87)).toEqual([0, 20, 40, 60, 80, 100])
    expect(usageTicks(3.5)).toEqual([0, 1, 2, 3, 4])
    expect(usageTicks(0.3)).toEqual([0, 0.1, 0.2, 0.3])
    expect(usageTicks(41_200_000)).toEqual([
      0, 10_000_000, 20_000_000, 30_000_000, 40_000_000, 50_000_000,
    ])
    expect(usageTicks(100)).toEqual([0, 20, 40, 60, 80, 100])
  })
})

describe('labelledColumns', () => {
  it('labels every column when they fit and always keeps the latest', () => {
    expect([...labelledColumns(7, 7)].sort((a, b) => a - b)).toEqual([0, 1, 2, 3, 4, 5, 6])
    expect([...labelledColumns(30, 7)].sort((a, b) => a - b)).toEqual([4, 9, 14, 19, 24, 29])
  })
})
