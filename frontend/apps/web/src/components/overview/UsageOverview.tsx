import type { UsageTimeseries } from '@omnara/sdk'
import type { ReactNode } from 'react'

import { OverviewSectionHeader } from '@/components/overview/OverviewSectionHeader'
import { ReportedCost } from '@/components/usage/ReportedCost'
import { formatUsageValue, usageMeasureValue } from '@/components/usage/usage-chart-data'
import { lastDaysUsageWindow } from '@/components/usage/usage-date-range'
import {
  UsageTimeseriesPanel,
  type UsageTimeseriesScope,
} from '@/components/usage/UsageTimeseriesPanel'
import { formatCount } from '@/lib/format'

const usageDays = 30

export function UsageOverview({
  filters,
  reportLink,
}: {
  filters: UsageTimeseriesScope
  reportLink?: ReactNode
}) {
  return (
    <section className="flex flex-col gap-6">
      <OverviewSectionHeader
        title="Usage"
        subtitle={`Last ${usageDays} days`}
        action={reportLink}
      />
      <UsageTimeseriesPanel
        filters={{ ...filters, ...lastDaysUsageWindow(usageDays), interval: 'day' }}
        summary={(timeseries) => <UsageFigures timeseries={timeseries} />}
        showControls={false}
      />
    </section>
  )
}

function UsageFigures({ timeseries }: { timeseries: UsageTimeseries }) {
  const { totals } = timeseries
  return (
    <div className="grid grid-cols-2 gap-x-6 gap-y-3 pb-3 sm:flex sm:flex-wrap sm:items-baseline sm:gap-x-10">
      <UsageFigure
        value={formatUsageValue('tokens', usageMeasureValue(totals, 'tokens'))}
        label="tokens"
      />
      <UsageFigure
        value={
          <ReportedCost
            modelCalls={totals.model_calls}
            cost={totals.cost}
            format={(decimal) => formatUsageValue('cost', Number(decimal))}
          />
        }
        label="cost"
      />
      <UsageFigure value={formatCount(totals.model_calls)} label="model calls" />
      <UsageFigure value={formatCount(timeseries.active_agents)} label="active agents" />
    </div>
  )
}

function UsageFigure({ value, label }: { value: ReactNode; label: string }) {
  return (
    <p className="flex min-w-0 flex-wrap items-baseline gap-x-2">
      <span className="text-xl font-semibold tracking-[-0.03em] sm:text-3xl">{value}</span>
      <span className="text-muted-foreground text-sm">{label}</span>
    </p>
  )
}
