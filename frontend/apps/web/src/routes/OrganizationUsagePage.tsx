import { useOrgUsage } from '@omnara/react'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'

import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { Button } from '@/components/ui/button'
import { defaultUsageRange, isDefaultUsageRange } from '@/components/usage/usage-date-range'
import { UsageDateRangeMenu } from '@/components/usage/UsageDateRangeMenu'
import { UsageReportView } from '@/components/usage/UsageReport'
import { UsageTimeseriesPanel } from '@/components/usage/UsageTimeseriesPanel'
import { useActiveOrg } from '@/lib/use-active-org'

export function OrganizationUsagePage() {
  const { activeOrg } = useActiveOrg()
  const [range, setRange] = useState(defaultUsageRange)
  const query = useOrgUsage(activeOrg.id, range.window)
  const filtered = !isDefaultUsageRange(range)

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb items={[{ id: 'usage', label: 'Usage' }]} />
      <section className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h1 className="type-title">Usage</h1>
          <UsageDateRangeMenu value={range} onChange={setRange} />
        </div>
        <UsageReportView
          query={query}
          chart={
            <UsageTimeseriesPanel
              filters={{ ...range.window, interval: range.interval, orgIDs: [activeOrg.id] }}
            />
          }
          emptyMessage={
            filtered
              ? 'No model usage in this time range.'
              : 'No model usage yet. Usage shows up here once you send a message to an agent.'
          }
          emptyAction={
            !filtered && (
              <Button asChild size="sm">
                <Link to="/agents" search={{ tab: 'instances' }}>
                  Go to agents
                </Link>
              </Button>
            )
          }
        />
      </section>
    </div>
  )
}
