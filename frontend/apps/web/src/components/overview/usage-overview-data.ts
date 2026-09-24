import type { OrgOverviewUsage, UsageTotals } from '@omnara/sdk'

import { formatCompactCount, formatCount, formatUsd } from '@/lib/format'

export const otherSeriesKey = 'other'
export const noProfileSeriesKey = 'no-profile'

const seriesColors = Array.from({ length: 8 }, (_, index) => `var(--chart-${index + 1})`)
const otherSeriesColor = 'var(--muted-foreground)'
const residualTolerance = 1e-9
const maxTickSteps = 5

export type UsageBreakdown = 'model' | 'profile'
export type UsageMeasure = 'tokens' | 'cost' | 'calls'

export interface UsageSeries {
  key: string
  name: string
  color: string
  total: number
}

export interface UsageColumn {
  start: Date
  total: number
  values: ReadonlyMap<string, number>
}

export interface UsageChartData {
  series: UsageSeries[]
  columns: UsageColumn[]
}

export function usageMeasureValue(totals: UsageTotals, measure: UsageMeasure) {
  if (measure === 'cost') return Number(totals.cost.provider_reported_usd)
  if (measure === 'calls') return totals.model_calls
  return totals.tokens.input_tokens_total + totals.tokens.output_tokens_total
}

function tokens(totals: UsageTotals) {
  return usageMeasureValue(totals, 'tokens')
}

function breakdownGroups(usage: OrgOverviewUsage, breakdown: UsageBreakdown) {
  if (breakdown === 'model') {
    return {
      groups: usage.models.map((model) => ({
        key: model.id,
        name: model.name,
        totals: model.totals,
      })),
      days: usage.days.map((day) =>
        day.models.map((model) => ({ key: model.id, tokens: model.tokens })),
      ),
    }
  }
  return {
    groups: usage.profiles.map((profile) => ({
      key: profile.id ?? noProfileSeriesKey,
      name: profile.name ?? 'No profile',
      totals: profile.totals,
    })),
    days: usage.days.map((day) =>
      day.profiles.map((profile) => ({
        key: profile.id ?? noProfileSeriesKey,
        tokens: profile.tokens,
      })),
    ),
  }
}

export function usageChartData(usage: OrgOverviewUsage, breakdown: UsageBreakdown): UsageChartData {
  const { groups, days } = breakdownGroups(usage, breakdown)
  const named = groups.map((group, index) => ({
    key: group.key,
    name: group.name,
    color: seriesColors[index] ?? otherSeriesColor,
    total: tokens(group.totals),
  }))
  const otherTotal = tokens(usage.totals) - named.reduce((sum, series) => sum + series.total, 0)
  const series =
    otherTotal > residualTolerance
      ? [
          ...named,
          { key: otherSeriesKey, name: 'Other', color: otherSeriesColor, total: otherTotal },
        ]
      : named
  const columns = usage.days.map((day, index) => {
    const values = new Map<string, number>()
    for (const group of days[index] ?? []) {
      values.set(group.key, group.tokens)
    }
    const columnTotal = tokens(day.totals)
    const grouped = [...values.values()].reduce((sum, value) => sum + value, 0)
    if (columnTotal - grouped > residualTolerance) {
      values.set(otherSeriesKey, columnTotal - grouped)
    }
    return { start: new Date(day.start), total: columnTotal, values }
  })
  return { series, columns }
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

const dayLabelFormatter = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' })
const dayTitleFormatter = new Intl.DateTimeFormat(undefined, {
  month: 'long',
  day: 'numeric',
  year: 'numeric',
})

export function formatDayLabel(start: Date) {
  return dayLabelFormatter.format(start)
}

export function formatDayTitle(start: Date) {
  return dayTitleFormatter.format(start)
}
