import { isAgentActive, useAgentProfiles, useAgents } from '@omnara/react'
import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'

import { AgentIcon } from '@/components/agents/AgentIcon'
import { CreateAgentProfileButton } from '@/components/agents/CreateAgentProfileButton'
import { AuroraPanel } from '@/components/aurora/AuroraPanel'
import {
  OverviewColumn,
  OverviewEmpty,
  OverviewList,
  OverviewRowLabel,
  overviewRowLimit,
  overviewRowLinkClass,
  OverviewRowTime,
} from '@/components/overview/OverviewColumn'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { agentIcon, profileIcon } from '@/lib/agent-icon'
import { errorMessage } from '@/lib/submit-status'
import { useProjectPage } from '@/lib/use-project-page'

interface ProjectScope {
  orgId: string
  projectId: string
}

export function ProjectOverviewSummary({ orgId, projectId }: ProjectScope) {
  return (
    // A translucent card lit by the page's backdrop, like the usage panel below it.
    <AuroraPanel className="grid w-full gap-8 p-3 sm:p-4 md:grid-cols-2 md:gap-10">
      <LatestAgentsColumn orgId={orgId} projectId={projectId} />
      <EditedProfilesColumn orgId={orgId} projectId={projectId} />
    </AuroraPanel>
  )
}

function LatestAgentsColumn({ orgId, projectId }: ProjectScope) {
  const query = useAgents(orgId, projectId, { sort: '-updated_at', pageSize: overviewRowLimit })
  const agents = useInfiniteQueryItems(query).slice(0, overviewRowLimit)
  const { project } = useProjectPage()
  return (
    <OverviewColumn title="Recent agents">
      <ColumnContent
        isPending={query.isPending}
        error={query.isError ? errorMessage(query.error, 'Could not load agents.') : undefined}
        emptyMessage={agents.length === 0 ? 'No agents yet' : undefined}
        emptyAction={
          project?.access.can_operate && (
            <Button asChild size="sm" variant="outline">
              <Link to="/projects/$projectId/agents" params={{ projectId }}>
                Launch an agent
              </Link>
            </Button>
          )
        }
      >
        <OverviewList>
          {agents.map((agent) => (
            <li key={agent.id} className="min-w-0">
              <Link
                to="/projects/$projectId/agents/$agentId/events"
                params={{ projectId, agentId: agent.id }}
                className={overviewRowLinkClass}
              >
                <AgentIcon
                  icon={agentIcon(agent.agent_profile_id, agent.id)}
                  animated={isAgentActive(agent)}
                  className="size-6"
                />
                <OverviewRowLabel name={agent.name === '' ? 'Agent' : agent.name} />
                <OverviewRowTime value={agent.updated_at} />
              </Link>
            </li>
          ))}
        </OverviewList>
      </ColumnContent>
    </OverviewColumn>
  )
}

function EditedProfilesColumn({ orgId, projectId }: ProjectScope) {
  const query = useAgentProfiles(orgId, projectId, {
    sort: '-updated_at',
    pageSize: overviewRowLimit,
  })
  const profiles = useInfiniteQueryItems(query).slice(0, overviewRowLimit)
  const { project } = useProjectPage()
  return (
    <OverviewColumn title="Recent profiles">
      <ColumnContent
        isPending={query.isPending}
        error={query.isError ? errorMessage(query.error, 'Could not load profiles.') : undefined}
        emptyMessage={profiles.length === 0 ? 'No profiles yet' : undefined}
        emptyAction={
          project?.access.can_manage && <CreateAgentProfileButton projectId={projectId} />
        }
      >
        <OverviewList>
          {profiles.map((profile) => (
            <li key={profile.id} className="min-w-0">
              <Link
                to="/projects/$projectId/agent-profiles/$profileId"
                params={{ projectId, profileId: profile.id }}
                className={overviewRowLinkClass}
              >
                <AgentIcon icon={profileIcon(profile.id)} className="size-6" />
                <OverviewRowLabel name={profile.name} />
                <OverviewRowTime value={profile.updated_at} />
              </Link>
            </li>
          ))}
        </OverviewList>
      </ColumnContent>
    </OverviewColumn>
  )
}

function ColumnContent({
  isPending,
  error,
  emptyMessage,
  emptyAction,
  children,
}: {
  isPending: boolean
  error?: string
  emptyMessage?: string
  emptyAction?: ReactNode
  children: ReactNode
}) {
  if (isPending) return <Skeleton className="h-36" />
  if (error) {
    return (
      <p className="text-destructive px-2 py-1.5 text-sm" role="alert">
        {error}
      </p>
    )
  }
  if (emptyMessage) return <OverviewEmpty message={emptyMessage} action={emptyAction} />
  return children
}
