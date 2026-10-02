import type { UsageTimeseries } from '@omnara/sdk'
import type { ReactNode } from 'react'

import { OverviewSectionHeader } from '@/components/overview/OverviewSectionHeader'
import { ReportedCost } from '@/components/usage/ReportedCost'
import { formatUsageValue, usageMeasureValue } from '@/components/usage/usage-chart-data'
import { lastDaysUsageWindow } from '@/components/usage/usage-date-range'
import { UsageStats } from '@/components/usage/UsageStats'
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
    <UsageStats
      stats={[
        { label: 'Tokens', value: formatUsageValue('tokens', usageMeasureValue(totals, 'tokens')) },
        {
          label: 'Cost',
          value: (
            <ReportedCost
              modelCalls={totals.model_calls}
              cost={totals.cost}
              format={(decimal) => formatUsageValue('cost', Number(decimal))}
            />
          ),
        },
        { label: 'Model calls', value: formatCount(totals.model_calls) },
        { label: 'Active agents', value: formatCount(timeseries.active_agents) },
      ]}
    />
  )
}
