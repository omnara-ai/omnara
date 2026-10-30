import { useOrgOverview } from '@omnara/react'
import type { OrgOverviewResponse } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'

import type { Blob } from '@/components/aurora/aurora'
import { DitherBackdrop } from '@/components/aurora/DitherBackdrop'
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

/** The landing hero's blobs, larger and lower so they sit behind the page's top content. */
const backdropBlobs: Blob[] = [
  { cx: 0.2, cy: 0.4, ax: 0.14, ay: 0.14, wx: 0.31, wy: 0.23, r: 0.4, peak: 0.6 },
  { cx: 0.84, cy: 0.44, ax: 0.12, ay: 0.12, wx: 0.19, wy: 0.29, r: 0.35, peak: 0.4 },
]

export function Overview() {
  const { activeOrg } = useActiveOrg()
  const overviewQuery = useOrgOverview(activeOrg.id)
  const onboarding = useOverviewOnboarding(activeOrg.id, overviewQuery.data)

  return (
    <div className="mx-auto flex h-full w-full max-w-5xl flex-col gap-12">
      {overviewQuery.isPending ? (
        <Skeleton className="h-28 rounded-xl" />
      ) : onboarding || overviewQuery.data ? (
        <>
          <DitherBackdrop
            className="absolute inset-x-0 top-0 h-[760px] sm:h-[900px]"
            blobs={backdropBlobs}
          />
          <div
            data-transparent-header
            className="relative flex min-h-0 flex-1 justify-center pt-20 pb-8 sm:pt-24 sm:pb-12"
          >
            {onboarding ? (
              <AgentOnboarding
                key={onboarding.key}
                orgId={activeOrg.id}
                project={onboarding.project}
                onAgentLaunched={onboarding.finish}
              />
            ) : (
              overviewQuery.data && (
                <OverviewContent orgId={activeOrg.id} overview={overviewQuery.data} />
              )
            )}
          </div>
        </>
      ) : (
        <OverviewError
          error={overviewQuery.error}
          onRetry={() => {
            void overviewQuery.refetch()
          }}
        />
      )}
    </div>
  )
}

/**
 * The project to onboard in, while the org has no agents yet. Once a profile shows up
 * during onboarding it stays up, so creating the profile doesn't close it before launch.
 * `finish` closes it once an agent is launched, leaving the overview.
 */
function useOverviewOnboarding(orgId: string, overview: OrgOverviewResponse | undefined) {
  const project = overview && onboardingProject(overview)
  const needsOnboarding = project != null && overview?.recent_agents.length === 0

  // Keyed by org and project so a profile seen during one org's onboarding doesn't keep
  // onboarding up after switching to another org.
  const key = project && `${orgId}:${project.id}`
  const [profileSeenKey, setProfileSeenKey] = useState<string>()
  const profileSeen = key !== undefined && profileSeenKey === key
  if (needsOnboarding && !profileSeen && overview.recent_agent_profiles.length > 0) {
    setProfileSeenKey(key)
  }
  const finish = () => {
    setProfileSeenKey(undefined)
  }
  return project && (needsOnboarding || profileSeen) ? { project, key, finish } : undefined
}

function OverviewContent({ orgId, overview }: { orgId: string; overview: OrgOverviewResponse }) {
  const { activeOrg } = useActiveOrg()
  return (
    <div className="flex w-full min-w-0 flex-col gap-14 sm:gap-16">
      <OverviewSummary overview={overview} />
      <UsageOverview
        orgId={orgId}
        aurora
        reportLink={
          canManageOrg(activeOrg.role) && (
            <Link to="/usage" className={cn(panelHintClass, 'inline-flex hover:underline')}>
              Usage report
              <ArrowRight className="size-3.5" aria-hidden="true" />
            </Link>
          )
        }
      />
    </div>
  )
}

function OverviewError({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  return (
    <div className="flex flex-col items-start gap-3">
      <p className="text-destructive text-sm" role="alert">
        {errorMessage(error, 'Could not load the overview.')}
      </p>
      <Button size="sm" variant="outline" onClick={onRetry}>
        Retry
      </Button>
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
