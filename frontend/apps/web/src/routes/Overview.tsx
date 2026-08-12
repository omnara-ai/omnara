import { useAgentProfiles, useAgents, useProjects } from '@omnara/react'

import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { FirstAgentCard } from '@/components/overview/FirstAgentCard'
import { RecentAgentsSection } from '@/components/overview/RecentAgents'
import { Skeleton } from '@/components/ui/skeleton'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useActiveOrg } from '@/lib/use-active-org'

export function Overview() {
  const { activeOrg } = useActiveOrg()
  const projectsQuery = useProjects(activeOrg.id)
  const projects = useInfiniteQueryItems(projectsQuery)

  const firstProject = projects.length === 1 ? projects[0] : undefined
  const onboardingProject = firstProject?.access.can_manage ? firstProject : undefined
  const profilesQuery = useAgentProfiles(activeOrg.id, onboardingProject?.id ?? '', {
    pageSize: 1,
    enabled: onboardingProject != null,
  })
  const agentsQuery = useAgents(activeOrg.id, onboardingProject?.id ?? '', {
    pageSize: 1,
    enabled: onboardingProject != null,
  })
  const overviewPending =
    onboardingProject != null && (profilesQuery.isPending || agentsQuery.isPending)
  const showOnboarding =
    onboardingProject != null &&
    profilesQuery.data?.pages[0]?.data.length === 0 &&
    agentsQuery.data?.pages[0]?.data.length === 0

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb items={[{ label: activeOrg.name }, { label: 'Overview' }]} />

      {overviewPending ? (
        <Skeleton className="h-28 rounded-xl" />
      ) : showOnboarding ? (
        <FirstAgentCard projectId={onboardingProject.id} />
      ) : (
        <RecentAgentsSection orgId={activeOrg.id} projects={projects} />
      )}
    </div>
  )
}
