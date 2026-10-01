import type {
  UsageTimeseries,
  UsageTimeseriesInterval,
  UsageTimeseriesMetric,
  UsageTimeseriesSeries,
  UsageTotals,
} from '@omnara/sdk'
import { addDays, addHours, addMonths, addWeeks } from 'date-fns'

import { type AgentIconSpec, profileIcon, tintColor } from '@/lib/agent-icon'
import { formatCompactCount, formatCount, formatUsd } from '@/lib/format'

export const otherSeriesKey = 'other'
export const noProfileSeriesKey = 'no-profile'
const allSeriesKey = 'all'

export const seriesColors = Array.from({ length: 8 }, (_, index) => `var(--chart-${index + 1})`)
export const otherSeriesColor = 'var(--muted-foreground)'
const noProfileSeriesColor = 'color-mix(in oklab, var(--muted-foreground) 45%, transparent)'
const residualTolerance = 1e-9
const maxTickSteps = 5

export type UsageBreakdown = 'model' | 'profile'
export type UsageMeasure = 'tokens' | 'cost' | 'calls'

export const usageMeasureMetrics: Record<UsageMeasure, UsageTimeseriesMetric> = {
  tokens: 'sum_tokens',
  cost: 'sum_cost',
  calls: 'count_model_calls',
}

export interface UsageSeries {
  key: string
  name: string
  color: string
  total: number
  icon?: AgentIconSpec
}

export interface UsageColumn {
  start: Date
  total: number
  values: ReadonlyMap<string, number>
}

export interface UsageChartData {
  measure: UsageMeasure
  interval: UsageTimeseriesInterval
  series: UsageSeries[]
  columns: UsageColumn[]
}

export function usageMeasureValue(totals: UsageTotals, measure: UsageMeasure) {
  if (measure === 'cost') return Number(totals.cost.provider_reported_usd)
  if (measure === 'calls') return totals.model_calls
  return totals.tokens.input_tokens_total + totals.tokens.output_tokens_total
}

function usageSeries(entry: UsageTimeseriesSeries, index: number): UsageSeries {
  const base = { total: entry.total }
  if (entry.kind === 'other') {
    return { ...base, key: otherSeriesKey, name: 'Other', color: otherSeriesColor }
  }
  if (entry.kind === 'no_profile') {
    return { ...base, key: noProfileSeriesKey, name: 'No profile', color: noProfileSeriesColor }
  }
  if (entry.kind === 'profile' && entry.id) {
    const icon = profileIcon(entry.id)
    return {
      ...base,
      key: entry.id,
      name: entry.name ?? entry.id,
      color: tintColor(icon.tint),
      icon,
    }
  }
  return {
    ...base,
    key: entry.id ?? allSeriesKey,
    name: entry.name ?? 'Total',
    color: seriesColors[index] ?? otherSeriesColor,
  }
}

export function usageChartData(timeseries: UsageTimeseries, measure: UsageMeasure): UsageChartData {
  const series = timeseries.series.map(usageSeries)
  const columns = timeseries.bucket_starts.map((start, index) => {
    const values = new Map<string, number>()
    for (const [seriesIndex, entry] of timeseries.series.entries()) {
      const value = entry.values[index] ?? 0
      const key = series[seriesIndex]?.key
      if (key !== undefined && value > 0) values.set(key, value)
    }
    const total = [...values.values()].reduce((sum, value) => sum + value, 0)
    return { start: new Date(start), total, values }
  })
  return { measure, interval: timeseries.interval, series, columns }
}

export function usageBucketEnd(start: Date, interval: UsageTimeseriesInterval) {
  if (interval === 'hour') return addHours(start, 1)
  if (interval === 'month') return addMonths(start, 1)
  if (interval === 'week') return addWeeks(start, 1)
  return addDays(start, 1)
}

export function usageTicks(max: number) {
  if (!(max > 0)) return [0]
  const magnitude = 10 ** Math.floor(Math.log10(max / maxTickSteps))
  const step =
    [1, 2, 2.5, 5, 10]
      .map((multiple) => multiple * magnitude)
      .find((candidate) => Math.ceil(max / candidate - residualTolerance) <= maxTickSteps) ??
    10 * magnitude
  const steps = Math.ceil(max / step - residualTolerance)
  return Array.from({ length: steps + 1 }, (_, index) => Number((index * step).toPrecision(12)))
}

export function labelledColumns(count: number, maxLabels: number) {
  const every = Math.max(1, Math.ceil(count / maxLabels))
  const indexes = new Set<number>()
  for (let index = count - 1; index >= 0; index -= every) {
    indexes.add(index)
  }
  return indexes
}

function costFractionDigits(value: number) {
  const magnitude = Math.abs(value)
  if (magnitude >= 1) return 2
  if (magnitude >= 0.01) return 3
  return 4
}

export function formatUsageValue(measure: UsageMeasure, value: number) {
  if (measure === 'cost') return formatUsd(value.toFixed(costFractionDigits(value)))
  if (measure === 'calls') return formatCount(value)
  return formatCompactCount(value)
}

const compactUsdFormatter = new Intl.NumberFormat(undefined, {
  style: 'currency',
  currency: 'USD',
  notation: 'compact',
  maximumFractionDigits: 2,
})

export function formatUsageAxisValue(measure: UsageMeasure, value: number) {
  if (measure === 'cost') return compactUsdFormatter.format(value)
  return formatCompactCount(value)
}

const dayLabelFormatter = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' })
const monthLabelFormatter = new Intl.DateTimeFormat(undefined, { month: 'short', year: 'numeric' })
const dayTitleFormatter = new Intl.DateTimeFormat(undefined, {
  month: 'long',
  day: 'numeric',
  year: 'numeric',
})
const monthTitleFormatter = new Intl.DateTimeFormat(undefined, { month: 'long', year: 'numeric' })
const hourLabelFormatter = new Intl.DateTimeFormat(undefined, { hour: 'numeric' })
const hourTitleFormatter = new Intl.DateTimeFormat(undefined, {
  month: 'long',
  day: 'numeric',
  hour: 'numeric',
  minute: '2-digit',
})

export function formatBucketLabel(start: Date, interval: UsageTimeseriesInterval) {
  if (interval === 'hour') return hourLabelFormatter.format(start)
  return interval === 'month' ? monthLabelFormatter.format(start) : dayLabelFormatter.format(start)
}

export function formatBucketTitle(start: Date, interval: UsageTimeseriesInterval) {
  if (interval === 'hour') return hourTitleFormatter.format(start)
  if (interval === 'month') return monthTitleFormatter.format(start)
  if (interval === 'week') return `Week of ${dayTitleFormatter.format(start)}`
  return dayTitleFormatter.format(start)
}
