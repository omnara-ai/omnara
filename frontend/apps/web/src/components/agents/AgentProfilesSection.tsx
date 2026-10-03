import {
  type AgentProfileListSort,
  useAgentProfiles,
  useAgentProfileUsage,
  useAgents,
  useCreateAgent,
  useOrgAgentProfiles,
} from '@omnara/react'
import { type Agent, type AgentProfileSummary, ApiError, type VisibleProject } from '@omnara/sdk'
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
import {
  CreateAgentProfileButton,
  OrgCreateAgentProfileButton,
} from '@/components/agents/CreateAgentProfileButton'
import { InsufficientCreditsMessage } from '@/components/agents/InsufficientCreditsMessage'
import { ProjectTag } from '@/components/agents/ProjectTag'
import { SlackOAuthOutcomeDialog } from '@/components/agents/SlackOAuthOutcomeDialog'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { TriangleAlert, Users } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { ReportedCost } from '@/components/usage/ReportedCost'
import { defaultUsageDays, lastDaysUsageWindow } from '@/components/usage/usage-date-range'
import { type PaginationControls, usePagedQuery } from '@/hooks/use-paged-query'
import { useProjectDirectory } from '@/hooks/use-project-directory'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { agentIcon, profileIcon } from '@/lib/agent-icon'
import { agentStatusLabel, isAgentActive } from '@/lib/agent-status'
import { formatCount, formatTimeAgo } from '@/lib/format'
import { isInsufficientCreditsError } from '@/lib/insufficient-credits'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'
import { useWebConfig } from '@/lib/web-config'

type ProfileListControls = ReturnType<typeof useResourceList<AgentProfileListSort>>

export function AgentProfilesSection({
  orgId,
  projectId,
  canOperate,
  canManage,
}: {
  orgId: string
  projectId: string
  canOperate: boolean
  canManage: boolean
}) {
  const list = useResourceList<AgentProfileListSort>('-updated_at')
  const query = useAgentProfiles(orgId, projectId, {
    filters: list.apiFilters,
    sort: list.sort,
  })
  const paged = usePagedQuery(query, list.queryKey)
  return (
    <AgentProfileList
      orgId={orgId}
      list={list}
      rows={paged.rows}
      pagination={paged.pagination}
      isPending={query.isPending}
      isError={query.isError}
      onRetry={() => {
        void query.refetch()
      }}
      canOperate={() => canOperate}
      emptyAction={canManage && <CreateAgentProfileButton projectId={projectId} />}
    />
  )
}

export function OrgAgentProfilesSection({ orgId }: { orgId: string }) {
  const list = useResourceList<AgentProfileListSort>('-updated_at')
  const query = useOrgAgentProfiles(orgId, { filters: list.apiFilters, sort: list.sort })
  const paged = usePagedQuery(query, list.queryKey)
  const { projects } = useProjectDirectory(orgId)
  const { activeOrg } = useActiveOrg()
  return (
    <AgentProfileList
      orgId={orgId}
      list={list}
      rows={paged.rows}
      pagination={paged.pagination}
      isPending={query.isPending}
      isError={query.isError}
      onRetry={() => {
        void query.refetch()
      }}
      canOperate={(profile) => projects.get(profile.project_id)?.access.can_operate ?? false}
      projectOf={(profile) => projects.get(profile.project_id)}
      emptyAction={
        <OrgCreateAgentProfileButton orgId={orgId} offerNewProject={canManageOrg(activeOrg.role)} />
      }
    />
  )
}

function AgentProfileList({
  orgId,
  list,
  rows,
  pagination,
  isPending,
  isError,
  onRetry,
  canOperate,
  projectOf,
  emptyAction,
}: {
  orgId: string
  list: ProfileListControls
  rows: AgentProfileSummary[]
  pagination: PaginationControls
  isPending: boolean
  isError: boolean
  onRetry: () => void
  canOperate: (profile: AgentProfileSummary) => boolean
  projectOf?: (profile: AgentProfileSummary) => VisibleProject | undefined
  emptyAction: ReactNode
}) {
  const { data: webConfig } = useWebConfig()
  const [launchingId, setLaunchingId] = useState<string | null>(null)
  const [launchError, setLaunchError] = useState<ApiError>()

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
          items={rows}
          getId={(profile) => profile.id}
          renderCard={(profile) => (
            <ProfileCard
              orgId={orgId}
              profile={profile}
              project={projectOf?.(profile)}
              action={
                canOperate(profile) && (
                  <LaunchProfileButton
                    orgId={orgId}
                    profile={profile}
                    disabled={launchingId !== null}
                    loading={launchingId === profile.id}
                    onStart={() => {
                      setLaunchError(undefined)
                      setLaunchingId(profile.id)
                    }}
                    onInsufficientCredits={setLaunchError}
                    onFinish={() => {
                      setLaunchingId(null)
                    }}
                  />
                )
              }
            />
          )}
          isFiltered={list.isFiltering}
          pagination={pagination}
          isPending={isPending}
          isError={isError}
          onRetry={onRetry}
          emptyMessage="No agent profiles yet. A profile is a saved, reusable agent config for launching agents in one click."
          emptyAction={emptyAction}
        />
      </div>
      <SlackOAuthOutcomeDialog />
    </>
  )
}

function LaunchProfileButton({
  orgId,
  profile,
  disabled,
  loading,
  onStart,
  onInsufficientCredits,
  onFinish,
}: {
  orgId: string
  profile: AgentProfileSummary
  disabled: boolean
  loading: boolean
  onStart: () => void
  onInsufficientCredits: (error: ApiError) => void
  onFinish: () => void
}) {
  const createAgent = useCreateAgent(orgId, profile.project_id)
  const navigate = useNavigate()

  async function launch() {
    onStart()
    try {
      const launched = await createAgent.mutateAsync({
        profile: profile.id,
        config: profile.current_config_id,
      })
      await navigate({
        to: '/projects/$projectId/agents/$agentId',
        params: { projectId: profile.project_id, agentId: launched.agent.id },
      })
    } catch (error) {
      if (isInsufficientCreditsError(error)) {
        onInsufficientCredits(error)
      } else {
        window.alert(error instanceof ApiError ? error.message : 'Could not launch agent')
      }
    }
    onFinish()
  }

  return (
    <Button
      type="button"
      size="sm"
      variant="ghost"
      className="text-primary hover:text-primary h-9 px-2 sm:h-7"
      disabled={disabled}
      loading={loading}
      onClick={() => {
        void launch()
      }}
    >
      Launch
    </Button>
  )
}

const instanceCountPageSize = 100
const recentAgentLimit = 5

function ProfileCard({
  orgId,
  profile,
  project,
  action,
}: {
  orgId: string
  profile: AgentProfileSummary
  project?: VisibleProject
  action: ReactNode
}) {
  const projectId = profile.project_id
  // The same window and subagent default as the profile's Usage tab, so the numbers match.
  const usage = useAgentProfileUsage(orgId, projectId, profile.id, {
    ...lastDaysUsageWindow(defaultUsageDays),
    includeSubagents: true,
  })
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
        <>
          <Link
            to="/projects/$projectId/agent-profiles/$profileId"
            params={{ projectId, profileId: profile.id }}
            className={agentCardLinkClass}
          >
            {profile.name}
          </Link>
          {project && <ProjectTag project={project} />}
        </>
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
            label={`cost, last ${String(defaultUsageDays)} days`}
            value={
              usage.data && (
                <ReportedCost
                  modelCalls={usage.data.totals.model_calls}
                  cost={usage.data.totals.cost}
                />
              )
            }
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
                animated={isAgentActive(agent)}
                className="size-5 rounded-[3px]"
              />
              <span className="truncate">{agent.name || 'Agent'}</span>
              {agent.activity?.state !== 'idle' && (
                <span className="text-muted-foreground shrink-0 text-xs">
                  {agentStatusLabel(agent)}
                </span>
              )}
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
