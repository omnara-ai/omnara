import {
  type ProjectModelGrantListSort,
  useDeleteProjectModelGrant,
  useProjectModelGrants,
} from '@omnara/react'
import { Link } from '@tanstack/react-router'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { GrantModelButton } from '@/components/projects/GrantModelButton'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { createdResourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { formatDateTime } from '@/lib/format'

export function ProjectModelGrantsTable({
  orgId,
  projectId,
}: {
  orgId: string
  projectId: string
}) {
  const list = useResourceList<ProjectModelGrantListSort>('-created_at')
  const grantsQuery = useProjectModelGrants(orgId, projectId, {
    filters: list.apiFilters,
    sort: list.sort,
  })
  const grantsPaged = usePagedQuery(grantsQuery, list.queryKey)
  const deleteGrant = useDeleteProjectModelGrant(orgId, projectId)

  return (
    <div className="flex flex-col gap-3">
      <SearchHeader
        title="Model grants"
        toolbar={
          <ResourceListToolbar
            search={list.search}
            onSearchChange={list.setSearch}
            sort={list.sort}
            sortOptions={createdResourceSortOptions}
            onSortChange={list.setSort}
            placeholder="Search model grants by name…"
          />
        }
      >
        <Button asChild size="sm" variant="ghost">
          <Link to="/models">Organization models</Link>
        </Button>
        <GrantModelButton />
      </SearchHeader>
      <DataTable
        columns={[
          { header: 'Model' },
          { header: 'Provider' },
          { header: '', className: 'w-14', isActions: true },
        ]}
        data={grantsPaged.rows}
        isFiltered={list.isFiltering}
        pagination={grantsPaged.pagination}
        getRowId={(item) => item.grant.id}
        rowCells={(item) => [
          <span className="font-medium">{item.model.name}</span>,
          <span className="text-muted-foreground">{item.model.provider_config || '—'}</span>,
          <ResourceRowActions
            deleteLabel="Delete grant"
            onDelete={() => {
              if (!window.confirm('Delete this model grant?')) return
              deleteGrant.mutate(item.grant.id)
            }}
          />,
        ]}
        rowExpanded={(item) => (
          <DetailList
            items={[
              { label: 'ID', value: item.grant.id, mono: true },
              { label: 'Configured model', value: item.grant.configured_model_id, mono: true },
              {
                label: 'Context window',
                value: item.grant.context_window_tokens
                  ? `${item.grant.context_window_tokens.toLocaleString()} tokens`
                  : 'Inherited',
              },
              {
                label: 'Max output',
                value: item.grant.max_output_tokens
                  ? `${item.grant.max_output_tokens.toLocaleString()} tokens`
                  : 'Inherited',
              },
              { label: 'Created', value: formatDateTime(item.grant.created_at) },
            ]}
          />
        )}
        isPending={grantsQuery.isPending}
        isError={grantsQuery.isError}
        onRetry={() => {
          void grantsQuery.refetch()
        }}
        emptyMessage="No models granted. Grant an organization model so agents in this project can use it."
      />
    </div>
  )
}
