import {
  type AgentProfileListSort,
  useAgentProfiles,
  useAgentProfileUsage,
  useAgents,
  useCreateAgent,
} from '@omnara/react'
import { type Agent, type AgentProfileSummary, ApiError } from '@omnara/sdk'
import { Link, useNavigate } from '@tanstack/react-router'
import { type ReactNode, useId, useState } from 'react'

import {
  AgentCard,
  agentCardLinkClass,
  AgentCardList,
  AgentCardStat,
  AgentCardStatToggle,
  AgentCardTime,
} from '@/components/agents/AgentCardList'
import { AgentIcon } from '@/components/agents/AgentIcon'
import { AgentStatus } from '@/components/agents/AgentStatus'
import { InsufficientCreditsMessage } from '@/components/agents/InsufficientCreditsMessage'
import { SlackOAuthOutcomeDialog } from '@/components/agents/SlackOAuthOutcomeDialog'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { TriangleAlert, Users } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { agentIcon, profileIcon } from '@/lib/agent-icon'
import { formatCount, formatTimeAgo, formatUsd } from '@/lib/format'
import { isInsufficientCreditsError } from '@/lib/insufficient-credits'
import { useWebConfig } from '@/lib/web-config'

export function AgentProfilesSection({
  orgId,
  projectId,
  canOperate,
}: {
  orgId: string
  projectId: string
  canOperate: boolean
}) {
  const list = useResourceList<AgentProfileListSort>('-updated_at')
  const query = useAgentProfiles(orgId, projectId, {
    filters: list.apiFilters,
    sort: list.sort,
  })
  const paged = usePagedQuery(query, list.queryKey)
  const createAgent = useCreateAgent(orgId, projectId)
  const { data: webConfig } = useWebConfig()
  const navigate = useNavigate()
  const [launchingId, setLaunchingId] = useState<string | null>(null)
  const [launchError, setLaunchError] = useState<ApiError>()

  async function launch(profile: AgentProfileSummary) {
    setLaunchError(undefined)
    setLaunchingId(profile.id)
    try {
      const launched = await createAgent.mutateAsync({
        profile: profile.id,
        config: profile.current_config_id,
      })
      await navigate({
        to: '/projects/$projectId/agents/$agentId',
        params: { projectId, agentId: launched.agent.id },
      })
    } catch (error) {
      if (isInsufficientCreditsError(error)) {
        setLaunchError(error)
      } else {
        window.alert(error instanceof ApiError ? error.message : 'Could not launch agent')
      }
    }
    setLaunchingId(null)
  }

  return (
    <>
      <div className="flex flex-col gap-3">
        {launchError && (
          <div
            className="border-destructive/30 bg-destructive/5 text-destructive flex items-start gap-2 rounded-md border px-3 py-2 text-sm"
            role="alert"
          >
            <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
            {webConfig?.billingURL ? (
              <InsufficientCreditsMessage billingHref={webConfig.billingHref} />
            ) : (
              launchError.message
            )}
          </div>
        )}
        <div className="flex">
          <ResourceListToolbar
            search={list.search}
            onSearchChange={list.setSearch}
            placeholder="Search profiles by name…"
            showSearch
            sort={{ value: list.sort, options: resourceSortOptions, onChange: list.setSort }}
          />
        </div>
        <AgentCardList
          items={paged.rows}
          getId={(profile) => profile.id}
          renderCard={(profile) => (
            <ProfileCard
              orgId={orgId}
              projectId={projectId}
              profile={profile}
              action={
                canOperate && (
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    className="text-primary hover:text-primary h-9 px-2 sm:h-7"
                    disabled={launchingId !== null}
                    loading={launchingId === profile.id}
                    onClick={() => {
                      void launch(profile)
                    }}
                  >
                    Launch
                  </Button>
                )
              }
            />
          )}
          isFiltered={list.isFiltering}
          pagination={paged.pagination}
          isPending={query.isPending}
          isError={query.isError}
          onRetry={() => {
            void query.refetch()
          }}
          emptyMessage="No agent profiles yet. A profile is a saved, reusable agent config for launching agents in one click."
        />
      </div>
      <SlackOAuthOutcomeDialog />
    </>
  )
}

const instanceCountPageSize = 100
const recentAgentLimit = 5

function ProfileCard({
  orgId,
  projectId,
  profile,
  action,
}: {
  orgId: string
  projectId: string
  profile: AgentProfileSummary
  action: ReactNode
}) {
  const usage = useAgentProfileUsage(orgId, projectId, profile.id, { includeSubagents: true })
  const instances = useAgents(orgId, projectId, {
    filters: { agent_profile_id: profile.id },
    sort: '-updated_at',
    pageSize: instanceCountPageSize,
  })
  const [expanded, setExpanded] = useState(false)
  const expansionId = useId()
  const firstPage = instances.data?.pages[0]
  const recentAgents = firstPage?.data.slice(0, recentAgentLimit) ?? []
  const instanceLabel =
    firstPage?.data.length === 1 && !firstPage.next_cursor ? 'instance' : 'instances'
  const instanceValue =
    firstPage && `${formatCount(firstPage.data.length)}${firstPage.next_cursor ? '+' : ''}`
  const hiddenAgentCount = (firstPage?.data.length ?? 0) - recentAgents.length
  const moreLabel =
    hiddenAgentCount > 0 || firstPage?.next_cursor
      ? `${formatCount(hiddenAgentCount)}${firstPage?.next_cursor ? '+' : ''} more`
      : undefined
  const expansion = {
    id: expansionId,
    open: expanded && recentAgents.length > 0,
    content: (
      <RecentAgentList
        projectId={projectId}
        profileId={profile.id}
        agents={recentAgents}
        moreLabel={moreLabel}
      />
    ),
  }
  return (
    <AgentCard
      icon={profileIcon(profile.id)}
      title={
        <Link
          to="/projects/$projectId/agent-profiles/$profileId"
          params={{ projectId, profileId: profile.id }}
          className={agentCardLinkClass}
        >
          {profile.name}
        </Link>
      }
      subtitle={
        <>
          <span className="truncate font-mono">{profile.current_config.model.name}</span>
          <span aria-hidden="true">·</span>
          <span className="truncate">{profile.current_config.model.provider_config}</span>
        </>
      }
      meta={
        <>
          <AgentCardTime label="Edited" value={profile.updated_at} />
          {action}
        </>
      }
      footer={
        <span className="tabular-nums">Version {formatCount(profile.current_generation)}</span>
      }
      stats={
        <>
          <AgentCardStat
            label="cost"
            value={usage.data && formatUsd(usage.data.totals.cost.provider_reported_usd)}
          />
          {recentAgents.length > 0 ? (
            <AgentCardStatToggle
              icon={Users}
              label={instanceLabel}
              value={instanceValue}
              expansion={expansion}
              onToggle={() => {
                setExpanded((open) => !open)
              }}
            />
          ) : (
            <AgentCardStat icon={Users} label={instanceLabel} value={instanceValue} />
          )}
        </>
      }
      expansion={expansion}
    />
  )
}

function RecentAgentList({
  projectId,
  profileId,
  agents,
  moreLabel,
}: {
  projectId: string
  profileId: string
  agents: Agent[]
  moreLabel: string | undefined
}) {
  return (
    <div className="flex flex-col gap-1 border-t px-2 py-2">
      <ul className="flex flex-col gap-0.5">
        {agents.map((agent) => (
          <li key={agent.id}>
            <Link
              to="/projects/$projectId/agents/$agentId"
              params={{ projectId, agentId: agent.id }}
              className="hover:bg-accent flex min-w-0 items-center gap-2.5 rounded-md px-2 py-1.5 text-sm transition-colors"
            >
              <AgentIcon
                icon={agentIcon(agent.agent_profile_id, agent.id)}
                className="size-5 rounded-[3px]"
              />
              <span className="truncate">{agent.name || 'Agent'}</span>
              {agent.activity?.state !== 'idle' && <AgentStatus agent={agent} />}
              <span className="text-muted-foreground ml-auto shrink-0 text-xs tabular-nums">
                {formatTimeAgo(agent.activity?.last_activity_at ?? agent.updated_at)}
              </span>
            </Link>
          </li>
        ))}
      </ul>
      {moreLabel && (
        <Link
          to="/projects/$projectId/agent-profiles/$profileId/agents"
          params={{ projectId, profileId }}
          className="text-muted-foreground hover:text-foreground self-end px-2 py-1 text-xs hover:underline"
        >
          {moreLabel}
        </Link>
      )}
    </div>
  )
}
