import { type ProjectAvailableSecretListSort, useProjectAvailableSecrets } from '@omnara/react'
import type { ProjectSecretAccess } from '@omnara/sdk'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { SecretRowActions } from '@/components/secrets/SecretRowActions'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { formatDateTime } from '@/lib/format'
import { secretSubtitle } from '@/lib/secrets'

function ownerLabel(access: ProjectSecretAccess) {
  return access.secret.owner.kind === 'org' ? 'Organization' : access.secret.owner.kind
}

/**
 * Secrets this project can use through a grant. Project-owned secrets live on
 * the Project Secrets page; grants are managed from the Organization tab on the Secrets page.
 */
export function ProjectSecretGrantsTable({
  orgId,
  projectId,
  projectName,
}: {
  orgId: string
  projectId: string
  projectName: string
}) {
  const list = useResourceList<ProjectAvailableSecretListSort>('-updated_at')
  const query = useProjectAvailableSecrets(orgId, projectId, {
    filters: { ...list.apiFilters, availability_source: 'grant' },
    sort: list.sort,
  })
  const paged = usePagedQuery(query, list.queryKey)

  return (
    <div className="flex flex-col gap-3">
      <SearchHeader
        title="Secret grants"
        toolbar={
          <ResourceListToolbar
            search={list.search}
            onSearchChange={list.setSearch}
            sort={list.sort}
            sortOptions={resourceSortOptions}
            onSortChange={list.setSort}
            placeholder="Search secret grants by name…"
          />
        }
      ></SearchHeader>
      <DataTable
        columns={[
          {
            id: 'secret',
            header: 'Secret',
            cell: (access) => <span className="font-medium">{access.secret.name}</span>,
          },
          {
            id: 'type',
            header: 'Type',
            cell: (access) => (
              <span className="text-muted-foreground">{secretSubtitle(access.secret)}</span>
            ),
          },
          {
            id: 'owner',
            header: 'Owner',
            className: 'w-32',
            cell: (access) => <span className="capitalize">{ownerLabel(access)}</span>,
          },
          {
            id: 'actions',
            header: '',
            className: 'w-14',
            isActions: true,
            cell: (access) => (
              <SecretRowActions
                orgId={orgId}
                secret={access.secret}
                availability={access.availability}
                projectName={projectName}
                canDelete
              />
            ),
          },
        ]}
        data={paged.rows}
        isFiltered={list.isFiltering}
        pagination={paged.pagination}
        getRowId={(access) => access.secret.id}
        rowExpanded={({ secret }) => (
          <DetailList
            items={[
              { label: 'ID', value: secret.id, mono: true },
              { label: 'MCP URL', value: secret.metadata.mcp_url, mono: true },
              { label: 'Payload keys', value: secret.payload_keys.join(', '), mono: true },
              { label: 'Version', value: secret.current_version_number },
              { label: 'Created', value: formatDateTime(secret.created_at) },
              { label: 'Updated', value: formatDateTime(secret.updated_at) },
            ]}
          />
        )}
        isPending={query.isPending}
        isError={query.isError}
        onRetry={() => {
          void query.refetch()
        }}
        emptyMessage="No secrets granted. Grant one from the Organization tab on the Secrets page."
      />
    </div>
  )
}
