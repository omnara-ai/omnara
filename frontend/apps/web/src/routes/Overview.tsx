import { useOrgOverview } from '@omnara/react'
import type { OrgOverviewResponse } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'

import { ArrowRight } from '@/components/icons'
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

  const manageableProject = overview && onboardingProject(overview)
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
            orgId={activeOrg.id}
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

/**
 * The project onboarding runs in: the one with the latest edited profile, so a profile
 * already made is where the first agent launches, or else the oldest project the caller
 * manages, which is the org's first. Projects are listed by activity, not creation.
 */
function onboardingProject(overview: OrgOverviewResponse) {
  const manageable = overview.projects.filter((project) => project.access.can_manage)
  const started = overview.recent_agent_profiles.find((profile) =>
    manageable.some((project) => project.id === profile.project_id),
  )
  if (started) return manageable.find((project) => project.id === started.project_id)
  return manageable
    .slice()
    .sort((left, right) => Date.parse(left.created_at) - Date.parse(right.created_at))[0]
}
