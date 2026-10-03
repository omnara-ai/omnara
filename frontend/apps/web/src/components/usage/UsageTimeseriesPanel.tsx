import { type UsageTimeseriesFilters, useUsageTimeseries } from '@omnara/react'
import type { UsageTimeseries } from '@omnara/sdk'
import { type ReactNode, useState } from 'react'

import { tabTriggerClass } from '@/components/overview/CodeBlock'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  type UsageBreakdown,
  usageChartData,
  type UsageMeasure,
  usageMeasureForMetric,
  usageMeasureMetrics,
} from '@/components/usage/usage-chart-data'
import { UsageChart } from '@/components/usage/UsageChart'
import { UsageStatsSkeleton } from '@/components/usage/UsageStats'
import { errorMessage } from '@/lib/submit-status'
import { cn } from '@/lib/utils'

export type UsageTimeseriesScope = Omit<
  UsageTimeseriesFilters,
  'timezone' | 'metric' | 'groupBy' | 'groupLimit'
>

const browserTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone

const breakdowns: { value: UsageBreakdown; label: string }[] = [
  { value: 'model', label: 'Model' },
  { value: 'profile', label: 'Profile' },
]

const measures: { value: UsageMeasure; label: string }[] = [
  { value: 'tokens', label: 'Tokens' },
  { value: 'cost', label: 'Cost' },
  { value: 'calls', label: 'Calls' },
]

export function UsageTimeseriesPanel({
  orgId,
  filters,
  summary,
  showControls = true,
}: {
  orgId: string
  filters: UsageTimeseriesScope
  summary?: (timeseries: UsageTimeseries) => ReactNode
  /** Measure and breakdown toggles; without them the chart shows tokens by model. */
  showControls?: boolean
}) {
  const [breakdown, setBreakdown] = useState<UsageBreakdown>('model')
  const [measure, setMeasure] = useState<UsageMeasure>('tokens')
  const query = useUsageTimeseries(orgId, {
    ...filters,
    timezone: browserTimezone,
    metric: usageMeasureMetrics[measure],
    groupBy: breakdown,
  })

  if (query.isPending) {
    return (
      <div className="flex flex-col gap-4">
        <Skeleton className="h-56 sm:h-72" />
        {summary && <UsageStatsSkeleton />}
      </div>
    )
  }
  if (query.isError) {
    return (
      <p className="text-destructive text-sm" role="alert">
        {errorMessage(query.error, 'Could not load usage.')}
      </p>
    )
  }
  const timeseries = query.data
  // While a new measure or breakdown loads, the previous payload is kept; draw it as what it is.
  const shownMeasure = usageMeasureForMetric(timeseries.metric)
  const shownBreakdown = timeseries.group_by ?? breakdown
  const measureLabel =
    measures.find((option) => option.value === shownMeasure)?.label ?? shownMeasure
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-4">
        {showControls && (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <UsageToggle
              label="Usage measure"
              value={measure}
              options={measures}
              onChange={setMeasure}
            />
            <UsageToggle
              label="Break usage down by"
              value={breakdown}
              options={breakdowns}
              onChange={setBreakdown}
            />
          </div>
        )}
        <div
          aria-busy={query.isPlaceholderData}
          className={cn('transition-opacity', query.isPlaceholderData && 'opacity-50')}
        >
          <UsageChart
            data={usageChartData(timeseries, shownMeasure)}
            label={`${measureLabel} per ${timeseries.interval} by ${shownBreakdown}`}
          />
        </div>
      </div>
      {summary?.(timeseries)}
    </div>
  )
}

function UsageToggle<TValue extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string
  value: TValue
  options: { value: TValue; label: string }[]
  onChange: (value: TValue) => void
}) {
  return (
    <Tabs
      value={value}
      onValueChange={(next) => {
        const option = options.find((candidate) => candidate.value === next)
        if (option) onChange(option.value)
      }}
    >
      <TabsList variant="line" aria-label={label} className="gap-1 p-0">
        {options.map((option) => (
          <TabsTrigger key={option.value} value={option.value} className={tabTriggerClass}>
            {option.label}
          </TabsTrigger>
        ))}
      </TabsList>
    </Tabs>
  )
}
