import { useProjectUsage } from '@omnara/react'

import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { UsageReportView } from '@/components/usage/UsageReport'

export function ProjectUsagePage() {
  return (
    <ProjectPageFrame title="Usage">
      {({ activeOrg, projectId }) => <ProjectUsage orgId={activeOrg.id} projectId={projectId} />}
    </ProjectPageFrame>
  )
}

function ProjectUsage({ orgId, projectId }: { orgId: string; projectId: string }) {
  const query = useProjectUsage(orgId, projectId)
  return (
    <section className="flex flex-col gap-4">
      <h1 className="type-title">Usage</h1>
      <UsageReportView query={query} />
    </section>
  )
}
