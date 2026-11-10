import type { UsageTimeseries } from '@omnara/sdk'
import type { ReactNode } from 'react'

import type { Blob } from '@/components/aurora/aurora'
import { AuroraPanel } from '@/components/aurora/AuroraPanel'
import { DitherBackdrop } from '@/components/aurora/DitherBackdrop'
import { OverviewSectionHeader } from '@/components/overview/OverviewSectionHeader'
import { formatUsageValue, usageMeasureValue } from '@/components/usage/usage-chart-data'
import { lastDaysUsageWindow } from '@/components/usage/usage-date-range'
import { UsageCost } from '@/components/usage/UsageCost'
import { UsageStats } from '@/components/usage/UsageStats'
import {
  UsageTimeseriesPanel,
  type UsageTimeseriesScope,
} from '@/components/usage/UsageTimeseriesPanel'
import { formatCount } from '@/lib/format'

const usageDays = 30

/**
 * A softer glow of its own behind the usage panel, which sits below the page backdrop, so
 * the translucent panel has something to blur. Blobs stay a radius from every edge.
 */
const usageBlobs: Blob[] = [
  { cx: 0.26, cy: 0.5, ax: 0.04, ay: 0.08, wx: 0.27, wy: 0.21, r: 0.2, peak: 0.45 },
  { cx: 0.76, cy: 0.5, ax: 0.04, ay: 0.08, wx: 0.17, wy: 0.31, r: 0.18, peak: 0.3 },
]

export function UsageOverview({
  orgId,
  filters = {},
  reportLink,
}: {
  orgId: string
  filters?: UsageTimeseriesScope
  reportLink?: ReactNode
}) {
  // Drawn in a panel lit by the page's backdrop, under a quiet label.
  return (
    <section className="flex flex-col gap-3 pb-10">
      <OverviewSectionHeader
        title="Usage"
        subtitle={`Last ${usageDays} days`}
        action={reportLink}
      />
      <div className="relative">
        <DitherBackdrop
          className="absolute inset-x-0 -inset-y-24"
          blobs={usageBlobs}
          fadeFrom={1}
          source="usage"
        />
        <AuroraPanel source="usage" className="p-5 sm:p-6">
          <UsageTimeseriesPanel
            orgId={orgId}
            filters={{ ...filters, ...lastDaysUsageWindow(usageDays), interval: 'day' }}
            summary={(timeseries) => <UsageFigures timeseries={timeseries} />}
            showControls={false}
          />
        </AuroraPanel>
      </div>
    </section>
  )
}

function UsageFigures({ timeseries }: { timeseries: UsageTimeseries }) {
  const { totals } = timeseries
  return (
    <UsageStats
      plain
      stats={[
        { label: 'Tokens', value: formatUsageValue('tokens', usageMeasureValue(totals, 'tokens')) },
        {
          label: 'Cost',
          value: (
            <UsageCost
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
