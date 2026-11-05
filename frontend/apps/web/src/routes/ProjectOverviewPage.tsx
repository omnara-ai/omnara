import { OverviewBackdrop } from '@/components/overview/OverviewBackdrop'
import { ProjectOverviewSummary } from '@/components/projects/ProjectOverviewSummary'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { ProjectUsageOverview } from '@/components/projects/ProjectUsageOverview'

export function ProjectOverviewPage() {
  return (
    <ProjectPageFrame>
      {({ activeOrg, projectId }) => (
        <OverviewBackdrop>
          <div className="flex w-full min-w-0 flex-col gap-14 sm:gap-16">
            <ProjectOverviewSummary orgId={activeOrg.id} projectId={projectId} />
            <ProjectUsageOverview orgId={activeOrg.id} projectId={projectId} />
          </div>
        </OverviewBackdrop>
      )}
    </ProjectPageFrame>
  )
}
