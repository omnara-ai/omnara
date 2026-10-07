import type { UsageTimeseries, UsageTotals } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import {
  formatBucketLabel,
  formatBucketTitle,
  labelledColumns,
  noProfileSeriesKey,
  otherSeriesKey,
  usageBucketEnd,
  usageChartData,
  usageMeasureForMetric,
  usageMeasureMetrics,
  usageMeasureValue,
  usageTicks,
} from '@/components/usage/usage-chart-data'
import { profileIcon, tintColor } from '@/lib/agent-icon'

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

function timeseries(series: UsageTimeseries['series']): UsageTimeseries {
  return {
    metric: 'sum_tokens',
    interval: 'day',
    group_by: 'model',
    timezone: 'UTC',
    bucket_starts: ['2026-09-21T00:00:00Z', '2026-09-22T00:00:00Z', '2026-09-23T00:00:00Z'],
    until: '2026-09-24T00:00:00Z',
    totals: totals(800, 90, '3.5', 10),
    active_agents: 3,
    series,
  }
}

describe('usageMeasureValue', () => {
  it('reads tokens as input plus output, cost as a number, and calls as counted', () => {
    const value = totals(120, 30, '0.0125', 3)
    expect(usageMeasureValue(value, 'tokens')).toBe(150)
    expect(usageMeasureValue(value, 'cost')).toBe(0.0125)
    expect(usageMeasureValue(value, 'calls')).toBe(3)
  })
})

describe('usageMeasureForMetric', () => {
  it('recovers the measure a timeseries was fetched for', () => {
    for (const measure of ['tokens', 'cost', 'calls'] as const) {
      expect(usageMeasureForMetric(usageMeasureMetrics[measure])).toBe(measure)
    }
    expect(usageMeasureForMetric('sum_input_tokens')).toBe('tokens')
  })
})

describe('usageChartData', () => {
  it('colors ranked models in order, keeps Other muted, and stacks every bucket', () => {
    const data = usageChartData(
      timeseries([
        { kind: 'model', id: opus, name: 'Opus', total: 550, values: [550, 0, 0] },
        { kind: 'model', id: sonnet, name: 'Sonnet', total: 330, values: [0, 0, 330] },
        { kind: 'other', total: 10, values: [0, 0, 10] },
      ]),
      'tokens',
    )
    expect(data.series.map((series) => [series.key, series.name, series.total])).toEqual([
      [opus, 'Opus', 550],
      [sonnet, 'Sonnet', 330],
      [otherSeriesKey, 'Other', 10],
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
    expect(data.interval).toBe('day')
    expect(data.measure).toBe('tokens')
  })

  it('gives profiles their icon tint and names usage without a profile', () => {
    const data = usageChartData(
      timeseries([
        { kind: 'profile', id: reviewer, name: 'Reviewer', total: 660, values: [550, 0, 110] },
        { kind: 'no_profile', total: 230, values: [0, 0, 230] },
      ]),
      'tokens',
    )
    expect(data.series.map((series) => [series.key, series.name, series.total])).toEqual([
      [reviewer, 'Reviewer', 660],
      [noProfileSeriesKey, 'No profile', 230],
    ])
    expect(data.series[0]?.color).toBe(tintColor(profileIcon(reviewer).tint))
    expect(data.series[0]?.icon).toEqual(profileIcon(reviewer))
    expect(data.series[1]?.icon).toBeUndefined()
    expect(Object.fromEntries(data.columns[2]?.values ?? [])).toEqual({
      [reviewer]: 110,
      [noProfileSeriesKey]: 230,
    })
  })

  it('names the project or provider only where series share a name', () => {
    const data = usageChartData(
      timeseries([
        {
          kind: 'profile',
          id: reviewer,
          name: 'Support',
          project_name: 'Web',
          total: 3,
          values: [1, 1, 1],
        },
        {
          kind: 'profile',
          id: opus,
          name: 'Support',
          project_name: 'API',
          total: 2,
          values: [1, 1, 0],
        },
        {
          kind: 'model',
          id: sonnet,
          name: 'Sonnet',
          model_provider_config_name: 'Anthropic',
          total: 1,
          values: [1, 0, 0],
        },
      ]),
      'tokens',
    )
    expect(data.series.map((series) => [series.name, series.detail])).toEqual([
      ['Support', 'Web'],
      ['Support', 'API'],
      ['Sonnet', undefined],
    ])
  })

  it('plots an ungrouped series as a single total', () => {
    const data = usageChartData(
      timeseries([{ kind: 'all', total: 1.5, values: [1, 0, 0.5] }]),
      'cost',
    )
    expect(data.series.map((series) => [series.key, series.name])).toEqual([['all', 'Total']])
    expect(data.columns.map((column) => column.total)).toEqual([1, 0, 0.5])
  })

  it('says when calls were made but no cost was reported, rather than no usage', () => {
    const unreported = { ...timeseries([{ kind: 'all', total: 0, values: [0, 0, 0] }]) }
    unreported.totals = {
      ...unreported.totals,
      cost: { provider_reported_usd: '0', model_calls_with_reported_cost: 0 },
    }
    expect(usageChartData(unreported, 'cost').emptyMessage).toBe('No cost reported for 10 calls')
    expect(usageChartData(unreported, 'tokens').emptyMessage).toBe('No usage')
    const idle = { ...unreported, totals: totals(0, 0, '0', 0) }
    expect(usageChartData(idle, 'cost').emptyMessage).toBe('No usage')
  })
})

describe('bucket formatting', () => {
  it('labels months by month and weeks by their first day', () => {
    const start = new Date(2026, 8, 1)
    expect(formatBucketLabel(start, 'month')).toContain('2026')
    expect(formatBucketTitle(start, 'week')).toMatch(/^Week of /)
    expect(usageBucketEnd(start, 'month')).toEqual(new Date(2026, 9, 1))
    expect(usageBucketEnd(start, 'week')).toEqual(new Date(2026, 8, 8))
    expect(usageBucketEnd(start, 'day')).toEqual(new Date(2026, 8, 2))
  })

  it('labels hours by time of day and ends them an hour later', () => {
    const start = new Date(2026, 8, 1, 15)
    expect(formatBucketLabel(start, 'hour')).not.toBe(formatBucketLabel(start, 'day'))
    expect(formatBucketTitle(start, 'hour')).not.toBe(formatBucketTitle(start, 'day'))
    expect(usageBucketEnd(start, 'hour')).toEqual(new Date(2026, 8, 1, 16))
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
