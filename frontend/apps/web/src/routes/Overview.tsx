import { useOrgOverview } from '@omnara/react'
import { useState } from 'react'

import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { AgentOnboarding } from '@/components/overview/AgentOnboarding'
import { RecentAgentsSection } from '@/components/overview/RecentAgents'
import { Skeleton } from '@/components/ui/skeleton'
import { useActiveOrg } from '@/lib/use-active-org'

export function Overview() {
  const { activeOrg } = useActiveOrg()
  const overviewQuery = useOrgOverview(activeOrg.id)
  const overview = overviewQuery.data

  const manageableProject = overview?.projects
    .slice()
    .reverse()
    .find((project) => project.access.can_manage)
  const needsOnboarding =
    overview != null && manageableProject != null && overview.recent_agents.length === 0

  const [profileSeen, setProfileSeen] = useState(false)
  if (needsOnboarding && !profileSeen && overview.recent_agent_profiles.length > 0) {
    setProfileSeen(true)
  }
  const showOnboarding =
    overview != null && manageableProject != null && (needsOnboarding || profileSeen)

  return (
    <div className="mx-auto flex h-full w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb
        items={[
          { id: 'organization', label: activeOrg.name },
          { id: 'overview', label: 'Overview' },
        ]}
      />

      {overviewQuery.isPending ? (
        <Skeleton className="h-28 rounded-xl" />
      ) : showOnboarding ? (
        <div className="flex min-h-0 flex-1 justify-center pb-8 sm:pb-16">
          <AgentOnboarding
            key={`${activeOrg.id}:${manageableProject.id}`}
            orgId={activeOrg.id}
            project={manageableProject}
          />
        </div>
      ) : (
        <RecentAgentsSection
          overview={overview}
          isError={overviewQuery.isError}
          onRetry={() => {
            void overviewQuery.refetch()
          }}
        />
      )}
    </div>
  )
}
