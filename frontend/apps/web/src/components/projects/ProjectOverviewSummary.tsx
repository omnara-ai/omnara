import { isAgentActive, useAgentProfileQuery, useAgentProfiles, useAgents } from '@omnara/react'
import type { Agent } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'

import { AgentIcon } from '@/components/agents/AgentIcon'
import { CreateAgentProfileButton } from '@/components/agents/CreateAgentProfileButton'
import {
  OverviewColumn,
  OverviewEmpty,
  OverviewList,
  OverviewRowLabel,
  overviewRowLimit,
  overviewRowLinkClass,
  OverviewRowTime,
} from '@/components/overview/OverviewColumn'
import { OverviewSectionHeader } from '@/components/overview/OverviewSectionHeader'
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
    <section className="flex flex-col gap-6">
      <OverviewSectionHeader title="Overview" />
      <div className="grid gap-4 md:grid-cols-2">
        <EditedProfilesColumn orgId={orgId} projectId={projectId} />
        <LatestAgentsColumn orgId={orgId} projectId={projectId} />
      </div>
    </section>
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
    <OverviewColumn title="Recently edited profiles">
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
                <AgentIcon icon={profileIcon(profile.id)} className="size-8" />
                <OverviewRowLabel
                  name={profile.name}
                  subtitle={profile.current_config.model.name}
                />
                <OverviewRowTime value={profile.updated_at} />
              </Link>
            </li>
          ))}
        </OverviewList>
      </ColumnContent>
    </OverviewColumn>
  )
}

function LatestAgentsColumn({ orgId, projectId }: ProjectScope) {
  const query = useAgents(orgId, projectId, { sort: '-updated_at', pageSize: overviewRowLimit })
  const agents = useInfiniteQueryItems(query).slice(0, overviewRowLimit)
  const { project } = useProjectPage()
  return (
    <OverviewColumn title="Latest agents">
      <ColumnContent
        isPending={query.isPending}
        error={query.isError ? errorMessage(query.error, 'Could not load agents.') : undefined}
        emptyMessage={agents.length === 0 ? 'No agents yet' : undefined}
        emptyAction={
          project?.access.can_operate && (
            <Button asChild size="sm">
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
              <LatestAgentRow orgId={orgId} projectId={projectId} agent={agent} />
            </li>
          ))}
        </OverviewList>
      </ColumnContent>
    </OverviewColumn>
  )
}

function LatestAgentRow({ orgId, projectId, agent }: ProjectScope & { agent: Agent }) {
  const { data: profile } = useAgentProfileQuery(orgId, projectId, agent.agent_profile_id)
  return (
    <Link
      to="/projects/$projectId/agents/$agentId/events"
      params={{ projectId, agentId: agent.id }}
      className={overviewRowLinkClass}
    >
      <AgentIcon
        icon={agentIcon(agent.agent_profile_id, agent.id)}
        animated={isAgentActive(agent)}
        className="size-8"
      />
      <OverviewRowLabel name={agent.name === '' ? 'Agent' : agent.name} subtitle={profile?.name} />
      <OverviewRowTime value={agent.updated_at} />
    </Link>
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
  if (isPending) return <Skeleton className="h-46" />
  if (error) {
    return (
      <p className="text-destructive text-sm" role="alert">
        {error}
      </p>
    )
  }
  if (emptyMessage) return <OverviewEmpty message={emptyMessage} action={emptyAction} />
  return children
}
