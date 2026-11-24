import { useAgentPanel, useOrgOverview } from '@omnara/react'

import { AssistantSetup } from '@/components/overview/AssistantSetup'
import { AssistantHome, AssistantInvite } from '@/components/overview/OverviewAssistant'
import { OverviewSummary } from '@/components/overview/OverviewSummary'
import { OverviewUsage } from '@/components/overview/OverviewUsage'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { canManageOrg } from '@/lib/permissions'
import { errorMessage } from '@/lib/submit-status'
import { useActiveOrg } from '@/lib/use-active-org'

const pageClass = 'mx-auto flex w-full max-w-6xl flex-col gap-6 lg:flex-row'
const railClass = 'flex w-full shrink-0 flex-col gap-4 lg:w-88'

export function Overview() {
  const { activeOrg } = useActiveOrg()
  const overviewQuery = useOrgOverview(activeOrg.id)
  const panelQuery = useAgentPanel()
  const overview = overviewQuery.data
  const panel = panelQuery.data
  const showReport = canManageOrg(activeOrg.role)

  if (overviewQuery.isPending || panelQuery.isPending) {
    return (
      <div className={pageClass}>
        <Skeleton className="h-96 rounded-xl lg:flex-1" />
        <div className={railClass}>
          <Skeleton className="h-44 rounded-xl" />
          <Skeleton className="h-72 rounded-xl" />
        </div>
      </div>
    )
  }
  if (!overview) {
    return (
      <div className="mx-auto flex w-full max-w-6xl flex-col items-start gap-3">
        <p className="text-destructive text-sm" role="alert">
          {errorMessage(overviewQuery.error, 'Could not load the overview.')}
        </p>
        <Button size="sm" variant="outline" onClick={() => void overviewQuery.refetch()}>
          Retry
        </Button>
      </div>
    )
  }
  if (panel?.selection != null) {
    return (
      <div className={`${pageClass} lg:h-full lg:min-h-0`}>
        <AssistantHome />
        <aside className={`${railClass} lg:overflow-y-auto`}>
          <OverviewSummary overview={overview} />
          <OverviewUsage orgId={activeOrg.id} showReport={showReport} compact />
        </aside>
      </div>
    )
  }
  // A new organization has nothing to summarize yet: its owner sets up the assistant first.
  if (panel != null && activeOrg.role === 'owner' && overview.recent_agents.length === 0) {
    return (
      <div className="flex min-h-full flex-col items-center justify-center pb-2 sm:pb-3">
        <AssistantSetup key={activeOrg.id} org={activeOrg} panel={panel} />
      </div>
    )
  }
  return (
    <div className={pageClass}>
      <div className="min-w-0 lg:flex-1">
        <OverviewUsage orgId={activeOrg.id} showReport={showReport} />
      </div>
      <aside className={railClass}>
        {panel != null && <AssistantInvite org={activeOrg} />}
        <OverviewSummary overview={overview} />
      </aside>
    </div>
  )
}
