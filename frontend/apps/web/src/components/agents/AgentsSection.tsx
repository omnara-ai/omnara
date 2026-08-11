import { type AgentListSort, useAgents, useArchiveAgent } from '@omnara/react'
import { type Agent, ApiError } from '@omnara/sdk'
import { useNavigate } from '@tanstack/react-router'

import { DataTable } from '@/components/data-table/DataTable'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { Badge } from '@/components/ui/badge'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'

export function AgentsSection({
  orgId,
  projectId,
  canManage,
}: {
  orgId: string
  projectId: string
  canManage: boolean
}) {
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-2xl font-bold tracking-tight">Agents</h2>
      </div>
      <AgentsTable
        orgId={orgId}
        projectId={projectId}
        canManage={canManage}
        emptyMessage="No agents yet. Launch one from a profile above, or create one with New agent."
      />
    </div>
  )
}

/** One page of a project's agents, optionally narrowed to one profile's launches. */
export function AgentsTable({
  orgId,
  projectId,
  canManage,
  profileId,
  emptyMessage,
}: {
  orgId: string
  projectId: string
  canManage: boolean
  profileId?: string
  emptyMessage: string
}) {
  const list = useResourceList<AgentListSort>('-updated_at')
  const query = useAgents(orgId, projectId, {
    filters: profileId ? { ...list.apiFilters, profile: profileId } : list.apiFilters,
    sort: list.sort,
  })
  const paged = usePagedQuery(query, list.queryKey)
  const showToolbar = list.isFiltering || paged.pagination.page > 0 || paged.pagination.canNext
  const archiveAgent = useArchiveAgent(orgId, projectId)
  const navigate = useNavigate()

  return (
    <div className="flex flex-col gap-3">
      {showToolbar && (
        <ResourceListToolbar
          search={list.search}
          onSearchChange={list.setSearch}
          sort={list.sort}
          sortOptions={resourceSortOptions}
          onSortChange={list.setSort}
          placeholder="Search agents by name…"
        />
      )}
      <DataTable
        columns={[
          { header: 'ID' },
          { header: 'Name' },
          { header: 'Model' },
          { header: 'Target' },
          { header: 'State', className: 'w-28' },
          { header: '', className: 'w-14', isActions: true },
        ]}
        data={paged.rows}
        isFiltered={list.isFiltering}
        pagination={paged.pagination}
        getRowId={(agent) => agent.id}
        rowCells={(agent) => [
          <span className="truncate font-mono text-xs">{agent.id}</span>,
          <span className="font-medium">{agent.name || 'Agent'}</span>,
          agent.model ? (
            <span className="flex min-w-0 flex-col">
              <span className="truncate">{agent.model.name}</span>
              <span className="text-muted-foreground truncate text-xs">
                {agent.model.provider_config}
              </span>
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
          <TargetCell agent={agent} />,
          <Badge variant="outline" className="capitalize">
            {agent.state}
          </Badge>,
          canManage ? (
            <ResourceRowActions
              deleteLabel="Archive"
              onDelete={() => {
                if (!window.confirm(`Archive ${agent.name || 'this agent'}?`)) return
                archiveAgent.mutate(agent.id, {
                  onError: (error) => {
                    window.alert(
                      error instanceof ApiError ? error.message : 'Could not archive agent',
                    )
                  },
                })
              }}
            />
          ) : null,
        ]}
        onRowClick={(agent) => {
          void navigate({
            to: '/projects/$projectId/agents/$agentId',
            params: { projectId, agentId: agent.id },
          })
        }}
        isPending={query.isPending}
        isError={query.isError}
        onRetry={() => {
          void query.refetch()
        }}
        emptyMessage={emptyMessage}
      />
    </div>
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
      className="text-muted-foreground hover:text-foreground hover:underline"
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
