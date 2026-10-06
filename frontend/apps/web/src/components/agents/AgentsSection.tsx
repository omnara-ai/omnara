import {
  type AgentListFilters,
  type AgentListSort,
  isAgentActive,
  useAgentProfileQuery,
  useAgents,
  useArchiveAgent,
  useOrgAgents,
} from '@omnara/react'
import { type Agent, ApiError, type VisibleProject } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { type ReactNode, useState } from 'react'

import {
  AgentCard,
  agentCardLinkClass,
  AgentCardList,
  AgentCardTime,
} from '@/components/agents/AgentCardList'
import { AgentIcon } from '@/components/agents/AgentIcon'
import { AgentOrigin } from '@/components/agents/AgentOrigin'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { Ellipsis, SettingsIcon } from '@/components/icons'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { ReportedCost } from '@/components/usage/ReportedCost'
import { type PaginationControls, usePagedQuery } from '@/hooks/use-paged-query'
import { useProjectDirectory } from '@/hooks/use-project-directory'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { agentIcon, profileIcon } from '@/lib/agent-icon'
import { agentStatusLabel } from '@/lib/agent-status'

type AgentListControls = ReturnType<typeof useAgentListControls>

function useAgentListControls() {
  const list = useResourceList<AgentListSort>('-updated_at')
  const [includeSubagents, setIncludeSubagents] = useState(false)
  const [includeArchived, setIncludeArchived] = useState(false)
  // Each card's cost, with its subagents, comes with the list rather than a request per card.
  const filters: AgentListFilters = { ...list.apiFilters, include_usage: true }
  if (includeSubagents) filters.include_subagents = true
  if (includeArchived) filters.include_archived = true
  return {
    list,
    filters,
    includeSubagents,
    setIncludeSubagents,
    includeArchived,
    setIncludeArchived,
    resetKey: `${list.queryKey}:${includeSubagents ? 'all' : 'top'}:${includeArchived ? 'archived' : 'active'}`,
  }
}

export function AgentsSection({
  orgId,
  projectId,
  canManage,
  profileId,
  emptyMessage,
  emptyAction,
}: {
  orgId: string
  projectId: string
  canManage: boolean
  profileId?: string
  emptyMessage: string
  emptyAction?: ReactNode
}) {
  const controls = useAgentListControls()
  const filters = profileId
    ? { ...controls.filters, agent_profile_id: profileId }
    : controls.filters
  const query = useAgents(orgId, projectId, { filters, sort: controls.list.sort })
  const paged = usePagedQuery(query, controls.resetKey)
  return (
    <AgentList
      orgId={orgId}
      controls={controls}
      rows={paged.rows}
      pagination={paged.pagination}
      isPending={query.isPending}
      isError={query.isError}
      onRetry={() => {
        void query.refetch()
      }}
      showProfile={profileId === undefined}
      canManage={() => canManage}
      emptyMessage={emptyMessage}
      emptyAction={emptyAction}
    />
  )
}

export function OrgAgentsSection({
  orgId,
  emptyMessage,
  emptyAction,
}: {
  orgId: string
  emptyMessage: string
  emptyAction?: ReactNode
}) {
  const controls = useAgentListControls()
  const query = useOrgAgents(orgId, { filters: controls.filters, sort: controls.list.sort })
  const paged = usePagedQuery(query, controls.resetKey)
  const { loaded, isLoaded } = useProjectDirectory(orgId)
  return (
    <AgentList
      orgId={orgId}
      controls={controls}
      rows={paged.rows}
      pagination={paged.pagination}
      isPending={query.isPending}
      isError={query.isError}
      onRetry={() => {
        void query.refetch()
      }}
      showProfile
      // Listed agents are in projects the viewer can read, so show actions until their
      // project loads (or if the list fails); the API still enforces access.
      canManage={(agent) => loaded.get(agent.project_id)?.access.can_manage ?? !isLoaded}
      projectOf={(agent) => loaded.get(agent.project_id)}
      emptyMessage={emptyMessage}
      emptyAction={emptyAction}
    />
  )
}

function AgentList({
  orgId,
  controls,
  rows,
  pagination,
  isPending,
  isError,
  onRetry,
  showProfile,
  canManage,
  projectOf,
  emptyMessage,
  emptyAction,
}: {
  orgId: string
  controls: AgentListControls
  rows: Agent[]
  pagination: PaginationControls
  isPending: boolean
  isError: boolean
  onRetry: () => void
  showProfile: boolean
  canManage: (agent: Agent) => boolean
  projectOf?: (agent: Agent) => VisibleProject | undefined
  emptyMessage: string
  emptyAction?: ReactNode
}) {
  const { list } = controls
  return (
    <div className="flex flex-col gap-3">
      <div className="flex">
        <ResourceListToolbar
          search={list.search}
          onSearchChange={list.setSearch}
          placeholder="Search agents by name…"
          showSearch
          sort={{ value: list.sort, options: resourceSortOptions, onChange: list.setSort }}
          filters={{
            subagents: {
              checked: controls.includeSubagents,
              onChange: controls.setIncludeSubagents,
            },
            archived: { checked: controls.includeArchived, onChange: controls.setIncludeArchived },
          }}
        />
      </div>
      <AgentCardList
        items={rows}
        getId={(agent) => agent.id}
        renderCard={(agent) => (
          <AgentInstanceCard
            orgId={orgId}
            agent={agent}
            project={projectOf?.(agent)}
            showProfile={showProfile}
            canManage={canManage(agent)}
          />
        )}
        isFiltered={list.isFiltering}
        pagination={pagination}
        isPending={isPending}
        isError={isError}
        onRetry={onRetry}
        emptyMessage={emptyMessage}
        emptyAction={emptyAction}
      />
    </div>
  )
}

function AgentInstanceCard({
  orgId,
  agent,
  project,
  showProfile,
  canManage,
}: {
  orgId: string
  agent: Agent
  project?: VisibleProject
  showProfile: boolean
  canManage: boolean
}) {
  const projectId = agent.project_id
  const archiveAgent = useArchiveAgent(orgId, projectId)

  function archive() {
    if (!window.confirm(`Archive ${agent.name || 'this agent'}?`)) return
    archiveAgent.mutate(agent.id, {
      onError: (error) => {
        window.alert(error instanceof ApiError ? error.message : 'Could not archive agent')
      },
    })
  }

  const target = agent.integration_target && <TargetCell key="target" agent={agent} />
  const subtitle = (
    <AgentOrigin
      orgId={orgId}
      agent={agent}
      project={project}
      showProfile={showProfile}
      after={target}
    />
  )
  return (
    <AgentCard
      icon={agentIcon(agent.agent_profile_id, agent.id)}
      animated={isAgentActive(agent)}
      title={
        <>
          <Link
            to="/projects/$projectId/agents/$agentId"
            params={{ projectId, agentId: agent.id }}
            className={agentCardLinkClass}
          >
            {agent.name || 'Agent'}
          </Link>
          {agent.parent_agent_id && (
            <Badge variant="outline" title={`Subagent of ${agent.parent_agent_id}`}>
              subagent
            </Badge>
          )}
        </>
      }
      subtitle={subtitle}
      meta={
        <>
          {agent.usage && (
            <span className="text-muted-foreground inline-flex shrink-0 items-center gap-1.5 text-xs tabular-nums">
              <ReportedCost modelCalls={agent.usage.model_calls} cost={agent.usage.cost} />
              <span className="ml-0.5" aria-hidden="true">
                ·
              </span>
            </span>
          )}
          <AgentCardTime
            label="Last active"
            value={agent.activity?.last_activity_at ?? agent.updated_at}
            status={agent.activity?.state === 'idle' ? undefined : agentStatusLabel(agent)}
          />
          <AgentActionsMenu
            orgId={orgId}
            projectId={projectId}
            agent={agent}
            showProfile={showProfile}
            onArchive={canManage ? archive : undefined}
          />
        </>
      }
    />
  )
}

function AgentActionsMenu({
  orgId,
  projectId,
  agent,
  showProfile,
  onArchive,
}: {
  orgId: string
  projectId: string
  agent: Agent
  showProfile: boolean
  onArchive?: () => void
}) {
  const profileId = showProfile ? agent.agent_profile_id : undefined
  const canEditConfig = agent.current_config_id !== undefined
  const hasLinks = profileId !== undefined || canEditConfig
  if (!hasLinks && !onArchive) return null
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" aria-label="Agent actions">
          <Ellipsis />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {profileId && <ProfileMenuItem orgId={orgId} projectId={projectId} profileId={profileId} />}
        {canEditConfig && (
          <DropdownMenuItem asChild>
            <Link
              to="/projects/$projectId/agents/$agentId/events"
              params={{ projectId, agentId: agent.id }}
              search={{ config: true }}
            >
              <SettingsIcon />
              Edit config
            </Link>
          </DropdownMenuItem>
        )}
        {onArchive && (
          <>
            {hasLinks && <DropdownMenuSeparator />}
            <DropdownMenuItem variant="destructive" onSelect={onArchive}>
              Archive
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function ProfileMenuItem({
  orgId,
  projectId,
  profileId,
}: {
  orgId: string
  projectId: string
  profileId: string
}) {
  const { data: profile } = useAgentProfileQuery(orgId, projectId, profileId)
  return (
    <DropdownMenuItem asChild>
      <Link to="/projects/$projectId/agent-profiles/$profileId" params={{ projectId, profileId }}>
        <AgentIcon icon={profileIcon(profileId)} className="size-4 rounded-[2px]" />
        <span className="truncate">{profile?.name ?? 'Agent profile'}</span>
      </Link>
    </DropdownMenuItem>
  )
}

function TargetCell({ agent }: { agent: Agent }) {
  const target = agent.integration_target
  if (!target) return <span className="text-muted-foreground">—</span>
  const label = integrationTargetLabel(target)
  if (!target.provider_uri) return <span className="text-muted-foreground">{label}</span>
  return (
    <a
      href={target.provider_uri}
      target="_blank"
      rel="noreferrer"
      className="text-muted-foreground hover:text-foreground relative truncate hover:underline"
      onClick={(event) => {
        event.stopPropagation()
      }}
    >
      {label}
    </a>
  )
}

// Where the agent is wired up, without provider-internal thread identifiers.
function integrationTargetLabel(target: NonNullable<Agent['integration_target']>) {
  const conversation = target.display_name.replace(/^#/, '')
  if (target.provider_ref_kind === 'dm') return 'Direct message'
  if (!conversation) return target.provider
  return target.provider_ref_kind === 'thread' ? `#${conversation} · thread` : `#${conversation}`
}
