import { type AgentProfileListSort, useAgentProfiles, useCreateAgent } from '@omnara/react'
import { type AgentProfile, ApiError } from '@omnara/sdk'
import { Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'

import { InsufficientCreditsMessage } from '@/components/agents/InsufficientCreditsMessage'
import { SlackOAuthOutcomeDialog } from '@/components/agents/SlackOAuthOutcomeDialog'
import { DataTable } from '@/components/data-table/DataTable'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { TriangleAlert } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { isInsufficientCreditsError } from '@/lib/insufficient-credits'
import { useWebConfig } from '@/lib/web-config'

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
  const showToolbar = list.isFiltering || paged.pagination.page > 0 || paged.pagination.canNext
  const createAgent = useCreateAgent(orgId, projectId)
  const { data: webConfig } = useWebConfig()
  const navigate = useNavigate()
  const [launchingId, setLaunchingId] = useState<string | null>(null)
  const [launchError, setLaunchError] = useState<ApiError>()

  async function launch(profile: AgentProfile) {
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
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-2xl font-bold tracking-tight">Agent profiles</h2>
          {canManage && (
            <Button asChild size="sm">
              <Link to="/projects/$projectId/agents/new" params={{ projectId }}>
                New agent
              </Link>
            </Button>
          )}
        </div>
        {showToolbar && (
          <ResourceListToolbar
            search={list.search}
            onSearchChange={list.setSearch}
            sort={list.sort}
            sortOptions={resourceSortOptions}
            onSortChange={list.setSort}
            placeholder="Search profiles by name…"
          />
        )}
        <DataTable
          columns={[
            {
              id: 'name',
              header: 'Name',
              cell: (profile) => <span className="font-medium">{profile.name}</span>,
            },
            {
              id: 'model',
              header: 'Model',
              cell: (profile) => (
                <span className="truncate font-mono text-xs">
                  {profile.current_config.model.name}
                </span>
              ),
            },
            {
              id: 'provider',
              header: 'Provider',
              cell: (profile) => (
                <span className="text-muted-foreground">
                  {profile.current_config.model.provider_config}
                </span>
              ),
            },
            {
              id: 'actions',
              header: '',
              className: 'w-24',
              isActions: true,
              revealOnHover: false,
              cell: (profile) => (
                <div className="flex items-center gap-1">
                  {canOperate && (
                    <Button
                      type="button"
                      size="sm"
                      variant="ghost"
                      className="text-primary hover:text-primary h-7 px-2"
                      disabled={launchingId !== null}
                      loading={launchingId === profile.id}
                      onClick={() => {
                        void launch(profile)
                      }}
                    >
                      Launch
                    </Button>
                  )}
                </div>
              ),
            },
          ]}
          data={paged.rows}
          isFiltered={list.isFiltering}
          pagination={paged.pagination}
          getRowId={(profile) => profile.id}
          onRowClick={(profile) => {
            void navigate({
              to: '/projects/$projectId/agent-profiles/$profileId',
              params: { projectId, profileId: profile.id },
            })
          }}
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
