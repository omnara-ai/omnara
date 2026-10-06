import { ProjectOverviewSummary } from '@/components/projects/ProjectOverviewSummary'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { ProjectUsageOverview } from '@/components/projects/ProjectUsageOverview'

export function ProjectOverviewPage() {
  return (
    <ProjectPageFrame>
      {({ activeOrg, projectId }) => (
        <div className="flex flex-col gap-12">
          <ProjectOverviewSummary orgId={activeOrg.id} projectId={projectId} />
          <ProjectUsageOverview orgId={activeOrg.id} projectId={projectId} />
        </div>
      )}
    </ProjectPageFrame>
  )
}
