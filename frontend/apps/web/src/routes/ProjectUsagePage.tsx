import { useProjectUsage } from '@omnara/react'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'

import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Button } from '@/components/ui/button'
import {
  defaultUsageDays,
  defaultUsageRange,
  isDefaultUsageRange,
} from '@/components/usage/usage-date-range'
import { UsageDateRangeMenu } from '@/components/usage/UsageDateRangeMenu'
import { UsageReportView } from '@/components/usage/UsageReport'
import { UsageTimeseriesPanel } from '@/components/usage/UsageTimeseriesPanel'

export function ProjectUsagePage() {
  return (
    <ProjectPageFrame title="Usage">
      {({ activeOrg, projectId }) => <ProjectUsage orgId={activeOrg.id} projectId={projectId} />}
    </ProjectPageFrame>
  )
}

function ProjectUsage({ orgId, projectId }: { orgId: string; projectId: string }) {
  const [range, setRange] = useState(defaultUsageRange)
  const query = useProjectUsage(orgId, projectId, range.window)
  const filtered = !isDefaultUsageRange(range)
  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="type-title">Usage</h1>
        <UsageDateRangeMenu value={range} onChange={setRange} />
      </div>
      <UsageReportView
        query={query}
        chart={
          <UsageTimeseriesPanel
            filters={{
              ...range.window,
              interval: range.interval,
              orgIDs: [orgId],
              projectIDs: [projectId],
            }}
          />
        }
        emptyMessage={
          filtered
            ? 'No model usage in this time range.'
            : `No model usage in the last ${String(defaultUsageDays)} days. Usage shows up here once you send a message to an agent.`
        }
        emptyAction={
          !filtered && (
            <Button asChild size="sm">
              <Link
                to="/projects/$projectId/agents"
                params={{ projectId }}
                search={{ tab: 'instances' }}
              >
                Go to agents
              </Link>
            </Button>
          )
        }
      />
    </section>
  )
}
