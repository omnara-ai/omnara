import { useOrgUsage } from '@omnara/react'
import { useState } from 'react'

import { FiltersMenu } from '@/components/data-table/FiltersMenu'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { allTimeUsageRange, usageWindowIsActive } from '@/components/usage/usage-date-range'
import { allProjectsUsageFilter } from '@/components/usage/usage-project-filter'
import { UsageReportView } from '@/components/usage/UsageReport'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function OrganizationUsagePage() {
  const { activeOrg } = useActiveOrg()
  if (!canManageOrg(activeOrg.role)) {
    return (
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-2">
        <h1 className="type-title">Not allowed</h1>
        <p className="text-muted-foreground text-sm">
          You don’t have permission to view usage for this organization.
        </p>
      </div>
    )
  }
  return <OrganizationUsage key={activeOrg.id} />
}

function OrganizationUsage() {
  const { activeOrg } = useActiveOrg()
  const [range, setRange] = useState(allTimeUsageRange)
  const [projectFilter, setProjectFilter] = useState(allProjectsUsageFilter)
  const projectIds = projectFilter.map((project) => project.id)
  const query = useOrgUsage(activeOrg.id, { ...range.window, includeProjectIDs: projectIds })
  const filtered = usageWindowIsActive(range.window) || projectIds.length > 0

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb
        items={[
          { id: 'organization', label: activeOrg.name, to: '/' },
          { id: 'usage', label: 'Usage' },
        ]}
      />
      <section className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h1 className="type-title">Usage</h1>
          <FiltersMenu
            dateRange={{ value: range, onChange: setRange }}
            projects={{ orgId: activeOrg.id, value: projectFilter, onChange: setProjectFilter }}
          />
        </div>
        <UsageReportView
          query={query}
          emptyMessage={filtered ? 'No model usage matches these filters.' : undefined}
        />
      </section>
    </div>
  )
}
