import type { OrgOverviewUsage, UsageTotals } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import {
  labelledColumns,
  noProfileSeriesKey,
  otherSeriesKey,
  usageChartData,
  usageMeasureValue,
  usageTicks,
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
const reviewer = 'aprf_aaaaaaaaaaaaaaaaaaaaaaaaaa'

const usage: OrgOverviewUsage = {
  totals: totals(800, 90, '3.5', 10),
  active_agents: 3,
  models: [
    { id: opus, name: 'Opus', totals: totals(500, 50, '2.5', 4) },
    { id: sonnet, name: 'Sonnet', totals: totals(300, 30, '0.75', 4) },
  ],
  profiles: [
    { id: reviewer, name: 'Reviewer', totals: totals(600, 60, '3', 6) },
    { totals: totals(200, 30, '0.5', 4) },
  ],
  days: [
    {
      start: '2026-09-21T07:00:00Z',
      totals: totals(500, 50, '2.5', 4),
      models: [{ id: opus, tokens: 550 }],
      profiles: [{ id: reviewer, tokens: 550 }],
    },
    { start: '2026-09-22T07:00:00Z', totals: totals(0, 0, '0', 0), models: [], profiles: [] },
    {
      start: '2026-09-23T07:00:00Z',
      totals: totals(300, 40, '1', 6),
      models: [{ id: sonnet, tokens: 330 }],
      profiles: [{ id: reviewer, tokens: 110 }, { tokens: 230 }],
    },
  ],
}

describe('usageMeasureValue', () => {
  it('reads tokens as input plus output, cost as a number, and calls as counted', () => {
    const value = totals(120, 30, '0.0125', 3)
    expect(usageMeasureValue(value, 'tokens')).toBe(150)
    expect(usageMeasureValue(value, 'cost')).toBe(0.0125)
    expect(usageMeasureValue(value, 'calls')).toBe(3)
  })
})

describe('usageChartData', () => {
  it('keeps ranked models, derives Other from the remainder, and stacks every day', () => {
    const data = usageChartData(usage, 'model')
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

  it('omits Other when the named models cover everything', () => {
    const covered = usageChartData({ ...usage, totals: totals(800, 80, '3.25', 8) }, 'model')
    expect(covered.series.map((series) => series.key)).toEqual([opus, sonnet])
  })

  it('breaks the same days down by profile and names usage without a profile', () => {
    const data = usageChartData(usage, 'profile')
    expect(data.series.map((series) => [series.key, series.name, series.total])).toEqual([
      [reviewer, 'Reviewer', 660],
      [noProfileSeriesKey, 'No profile', 230],
    ])
    expect(data.columns.map((column) => column.total)).toEqual([550, 0, 340])
    expect(Object.fromEntries(data.columns[2]?.values ?? [])).toEqual({
      [reviewer]: 110,
      [noProfileSeriesKey]: 230,
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
