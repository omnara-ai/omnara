import { type AgentListSort, useAgents, useArchiveAgent } from '@omnara/react'
import { type Agent, ApiError } from '@omnara/sdk'
import { Link, useNavigate } from '@tanstack/react-router'

import { DataTable } from '@/components/data-table/DataTable'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'

export function AgentsSection({
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
  const list = useResourceList<AgentListSort>('-updated_at')
  const query = useAgents(orgId, projectId, {
    filters: list.apiFilters,
    sort: list.sort,
  })
  const paged = usePagedQuery(query, list.queryKey)
  const archiveAgent = useArchiveAgent(orgId, projectId)
  const navigate = useNavigate()

  const newAgentButton = () =>
    canOperate ? (
      <Button asChild size="sm">
        <Link to="/projects/$projectId/agents/new" params={{ projectId }}>
          New agent
        </Link>
      </Button>
    ) : undefined

  return (
    <div className="flex flex-col gap-3">
      <SearchHeader
        title="Agents"
        toolbar={
          <ResourceListToolbar
            search={list.search}
            onSearchChange={list.setSearch}
            sort={list.sort}
            sortOptions={resourceSortOptions}
            onSortChange={list.setSort}
            placeholder="Search agents by name…"
          />
        }
      >
        {newAgentButton()}
      </SearchHeader>
      <DataTable
        columns={[
          {
            id: 'name',
            header: 'Name',
            cell: (agent) => <span className="font-medium">{agent.name || 'Agent'}</span>,
          },
          {
            id: 'model',
            header: 'Model',
            cell: (agent) =>
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
          },
          { id: 'target', header: 'Target', cell: (agent) => <TargetCell agent={agent} /> },
          {
            id: 'state',
            header: 'State',
            className: 'w-28',
            cell: (agent) => (
              <Badge variant="outline" className="capitalize">
                {agent.state}
              </Badge>
            ),
          },
          {
            id: 'actions',
            header: '',
            className: 'w-14',
            isActions: true,
            cell: (agent) =>
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
          },
        ]}
        data={paged.rows}
        isFiltered={list.isFiltering}
        pagination={paged.pagination}
        getRowId={(agent) => agent.id}
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
        emptyMessage="No agents yet. Launch one from a profile or a YAML config."
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
