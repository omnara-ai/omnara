import { Link } from '@tanstack/react-router'

import { ArrowRight } from '@/components/icons'
import { UsageFigures } from '@/components/overview/UsageOverview'
import { lastDaysUsageWindow } from '@/components/usage/usage-date-range'
import { UsageTimeseriesPanel } from '@/components/usage/UsageTimeseriesPanel'

const usageDays = 30

export function OverviewUsage({
  orgId,
  showReport,
  compact = false,
}: {
  orgId: string
  showReport: boolean
  /** A short chart with two figures per row, for the narrow column. */
  compact?: boolean
}) {
  return (
    <section className="bg-card flex flex-col gap-4 rounded-xl border p-5">
      <div className="flex items-baseline gap-2">
        <h3 className="text-muted-foreground text-xs font-medium">Usage</h3>
        <p className="text-muted-foreground/70 text-xs">Last {usageDays} days</p>
        {showReport && (
          <Link
            to="/usage"
            className="text-muted-foreground hover:text-foreground ml-auto inline-flex items-center gap-1 text-xs"
          >
            Report
            <ArrowRight className="size-3" aria-hidden="true" />
          </Link>
        )}
      </div>
      <UsageTimeseriesPanel
        orgId={orgId}
        filters={{ ...lastDaysUsageWindow(usageDays), interval: 'day' }}
        summary={(timeseries) => <UsageFigures timeseries={timeseries} narrow={compact} />}
        showControls={false}
        compact={compact}
      />
    </section>
  )
}
