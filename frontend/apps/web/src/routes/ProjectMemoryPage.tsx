import { useMemoryStores } from '@omnara/react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { MemoryStoreDialog } from '@/components/memory/MemoryStoreDialog'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { useListToolbarVisibility, useResourceList } from '@/hooks/use-resource-list'
import { guides } from '@/lib/docs'
import { formatDateTime } from '@/lib/format'

export function ProjectMemoryPage() {
  return (
    <ProjectPageFrame title="Memory">
      {({ activeOrg, projectId, project }) =>
        project?.access.can_read ? (
          <MemoryStores
            key={projectId}
            orgId={activeOrg.id}
            projectId={projectId}
            canManage={project.access.can_manage}
          />
        ) : (
          <p className="text-muted-foreground text-sm">
            You don’t have permission to view memory stores here.
          </p>
        )
      }
    </ProjectPageFrame>
  )
}

function MemoryStores({
  orgId,
  projectId,
  canManage,
}: {
  orgId: string
  projectId: string
  canManage: boolean
}) {
  const list = useResourceList('name')
  const query = useMemoryStores(orgId, projectId, { filters: list.apiFilters })
  const paged = usePagedQuery(query, list.queryKey)
  const showToolbar = useListToolbarVisibility(list, paged.pagination, query.isSuccess)
  const navigate = useNavigate()
  const [creating, setCreating] = useState(false)
  return (
    <>
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
          {canManage && (
            <Button
              size="sm"
              onClick={() => {
                setCreating(true)
              }}
            >
              Create store
            </Button>
          )}
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
                  params={{ projectId, storeId: store.id }}
                  onClick={(event) => {
                    event.stopPropagation()
                  }}
                >
                  {store.name}
                </Link>
              ),
            },
            {
              id: 'description',
              header: 'Description',
              cell: (store) => (
                <span className="text-muted-foreground line-clamp-2">
                  {store.description || '—'}
                </span>
              ),
            },
            {
              id: 'access',
              header: 'Agent access',
              cell: (store) => (store.read_only ? 'Read-only' : 'Read & write'),
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
              params: { projectId, storeId: store.id },
            })
          }
        />
      </div>
      {creating && (
        <MemoryStoreDialog
          orgId={orgId}
          projectId={projectId}
          onClose={() => {
            setCreating(false)
          }}
        />
      )}
    </>
  )
}
