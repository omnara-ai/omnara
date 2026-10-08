import type { MemoryStore, MemoryStoreList, VisibleProject } from '@omnara/sdk'
import type { InfiniteData, UseInfiniteQueryResult } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import type { ReactNode } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { useListToolbarVisibility, type useResourceList } from '@/hooks/use-resource-list'
import { guides } from '@/lib/docs'
import { formatDateTime } from '@/lib/format'

export function MemoryStoresSection({
  list,
  query,
  action,
  projectOf,
}: {
  list: ReturnType<typeof useResourceList<'name'>>
  query: UseInfiniteQueryResult<InfiniteData<MemoryStoreList>, unknown>
  action?: ReactNode
  projectOf?: (store: MemoryStore) => VisibleProject | undefined
}) {
  const paged = usePagedQuery(query, list.queryKey)
  const showToolbar = useListToolbarVisibility(list, paged.pagination, query.isSuccess)
  const navigate = useNavigate()
  return (
    <div className="flex flex-col gap-3">
      <SearchHeader
        title="Memory"
        guide={guides.memory}
        toolbar={
          <ResourceListToolbar
            search={list.search}
            onSearchChange={list.setSearch}
            placeholder="Search stores by name…"
            showSearch={showToolbar}
          />
        }
      >
        {action}
      </SearchHeader>
      <DataTable
        columns={[
          {
            id: 'name',
            header: 'Name',
            cell: (store) => (
              <Link
                className="font-medium"
                to="/projects/$projectId/memory/$storeId"
                params={{ projectId: store.project_id, storeId: store.id }}
                onClick={(event) => {
                  event.stopPropagation()
                }}
              >
                {store.name}
              </Link>
            ),
          },
          ...(projectOf
            ? [
                {
                  id: 'project',
                  header: 'Project',
                  cell: (store: MemoryStore) => (
                    <span className="text-muted-foreground">{projectOf(store)?.name}</span>
                  ),
                },
              ]
            : []),
          {
            id: 'description',
            header: 'Description',
            cell: (store) => (
              <span className="text-muted-foreground line-clamp-2">{store.description || '—'}</span>
            ),
          },
          {
            id: 'access',
            header: 'Agent access',
            cell: (store) => (store.agent_access === 'read' ? 'Read-only' : 'Read & write'),
          },
          {
            id: 'updated',
            header: 'Updated',
            cell: (store) => (
              <span className="text-muted-foreground text-sm">
                {formatDateTime(store.updated_at)}
              </span>
            ),
          },
        ]}
        data={paged.rows}
        getRowId={(store) => store.id}
        pagination={paged.pagination}
        isFiltered={list.isFiltering}
        isPending={query.isPending}
        isError={query.isError}
        onRetry={() => void query.refetch()}
        emptyMessage="No memory stores yet. Create one to share files with your agents."
        onRowClick={(store) =>
          void navigate({
            to: '/projects/$projectId/memory/$storeId',
            params: { projectId: store.project_id, storeId: store.id },
          })
        }
      />
    </div>
  )
}
