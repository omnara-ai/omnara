import { useProjectUsage } from '@omnara/react'
import { useState } from 'react'

import { FiltersMenu } from '@/components/data-table/FiltersMenu'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { allTimeUsageRange, usageWindowIsActive } from '@/components/usage/usage-date-range'
import { UsageReportView } from '@/components/usage/UsageReport'

export function ProjectUsagePage() {
  return (
    <ProjectPageFrame title="Usage">
      {({ activeOrg, projectId }) => <ProjectUsage orgId={activeOrg.id} projectId={projectId} />}
    </ProjectPageFrame>
  )
}

function ProjectUsage({ orgId, projectId }: { orgId: string; projectId: string }) {
  const [range, setRange] = useState(allTimeUsageRange)
  const query = useProjectUsage(orgId, projectId, range.window)
  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="type-title">Usage</h1>
        <FiltersMenu dateRange={{ value: range, onChange: setRange }} />
      </div>
      <UsageReportView
        query={query}
        emptyMessage={
          usageWindowIsActive(range.window) ? 'No model usage in this time range.' : undefined
        }
      />
    </section>
  )
}
