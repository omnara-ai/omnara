import { useOrgOverview } from '@omnara/react'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'

import { ArrowRight } from '@/components/icons'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { AgentOnboarding } from '@/components/overview/AgentOnboarding'
import { panelHintClass } from '@/components/overview/CodeBlock'
import { OverviewSummary } from '@/components/overview/OverviewSummary'
import { UsageOverview } from '@/components/overview/UsageOverview'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { canManageOrg } from '@/lib/permissions'
import { errorMessage } from '@/lib/submit-status'
import { useActiveOrg } from '@/lib/use-active-org'
import { cn } from '@/lib/utils'

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

  // Keyed by org and project so a profile seen during one org's onboarding doesn't keep
  // onboarding up after switching to another org.
  const onboardingKey = manageableProject && `${activeOrg.id}:${manageableProject.id}`
  const [profileSeenKey, setProfileSeenKey] = useState<string>()
  const profileSeen = onboardingKey !== undefined && profileSeenKey === onboardingKey
  if (needsOnboarding && !profileSeen && overview.recent_agent_profiles.length > 0) {
    setProfileSeenKey(onboardingKey)
  }
  const showOnboarding =
    overview != null && manageableProject != null && (needsOnboarding || profileSeen)

  return (
    <div className="mx-auto flex h-full w-full max-w-5xl flex-col gap-12">
      <PageBreadcrumb items={[{ id: 'overview', label: 'Overview' }]} />

      {overviewQuery.isPending ? (
        <Skeleton className="h-28 rounded-xl" />
      ) : showOnboarding ? (
        <div className="flex min-h-0 flex-1 justify-center pb-8 sm:pb-16">
          <AgentOnboarding key={onboardingKey} orgId={activeOrg.id} project={manageableProject} />
        </div>
      ) : overview ? (
        <>
          <OverviewSummary overview={overview} />
          <UsageOverview
            filters={{ orgIDs: [activeOrg.id] }}
            reportLink={
              canManageOrg(activeOrg.role) && (
                <Link to="/usage" className={cn(panelHintClass, 'inline-flex hover:underline')}>
                  Usage report
                  <ArrowRight className="size-3.5" aria-hidden="true" />
                </Link>
              )
            }
          />
        </>
      ) : (
        <div className="flex flex-col items-start gap-3">
          <p className="text-destructive text-sm" role="alert">
            {errorMessage(overviewQuery.error, 'Could not load the overview.')}
          </p>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              void overviewQuery.refetch()
            }}
          >
            Retry
          </Button>
        </div>
      )}
    </div>
  )
}
