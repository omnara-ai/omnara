import { useProjectUsage } from '@omnara/react'

import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { UsageReportView } from '@/components/usage/UsageReport'

export function ProjectUsagePage() {
  return (
    <ProjectPageFrame title="Usage">
      {({ activeOrg, projectId, project }) => (
        <ProjectUsage
          orgId={activeOrg.id}
          projectId={projectId}
          projectName={project?.name ?? ''}
        />
      )}
    </ProjectPageFrame>
  )
}

function ProjectUsage({
  orgId,
  projectId,
  projectName,
}: {
  orgId: string
  projectId: string
  projectName: string
}) {
  const query = useProjectUsage(orgId, projectId)
  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-col gap-1">
        <h1 className="type-title">Usage</h1>
        <p className="text-muted-foreground text-sm">
          Model tokens and provider-reported cost across every agent in {projectName}, including
          subagents.
        </p>
      </div>
      <UsageReportView query={query} />
    </section>
  )
}
