import {
  type ModelProviderListSort,
  useDeleteModelProvider,
  useModelProviders,
} from '@omnara/react'
import { ApiError, type ModelProviderConfig } from '@omnara/sdk'
import { useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { CreateModelProviderDialog } from '@/components/org/CreateModelProviderDialog'
import { EditModelProviderDialog } from '@/components/org/EditModelProviderDialog'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { formatDateTime } from '@/lib/format'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function ModelProvidersSection() {
  const { activeOrg } = useActiveOrg()
  const canManage = canManageOrg(activeOrg.role)
  const list = useResourceList<ModelProviderListSort>('-created_at')
  const query = useModelProviders(activeOrg.id, { filters: list.apiFilters, sort: list.sort })
  const paged = usePagedQuery(query, list.queryKey)
  const deleteProvider = useDeleteModelProvider(activeOrg.id)
  const [open, setOpen] = useState(false)
  const [editProvider, setEditProvider] = useState<ModelProviderConfig | null>(null)

  const newProviderButton = () =>
    canManage ? (
      <Button
        size="sm"
        onClick={() => {
          setOpen(true)
        }}
      >
        New provider
      </Button>
    ) : undefined

  return (
    <>
      <div className="flex flex-col gap-3">
        <SearchHeader
          title="Model providers"
          toolbar={
            <ResourceListToolbar
              search={list.search}
              onSearchChange={list.setSearch}
              sort={list.sort}
              sortOptions={resourceSortOptions}
              onSortChange={list.setSort}
              placeholder="Search providers by name…"
            />
          }
        >
          {newProviderButton()}
        </SearchHeader>
        <DataTable
          columns={[
            {
              id: 'name',
              header: 'Name',
              cell: (provider) => (
                <span className="inline-flex max-w-full items-center gap-2">
                  <span className="truncate font-medium">{provider.name}</span>
                  {provider.management_kind === 'cluster' && (
                    <span className="text-muted-foreground">cluster</span>
                  )}
                </span>
              ),
            },
            {
              id: 'api-format',
              header: 'API format',
              className: 'w-36',
              cell: (provider) => provider.api_format,
            },
            {
              id: 'base-url',
              header: 'Base URL',
              cell: (provider) => (
                <span className="text-muted-foreground">{provider.base_url}</span>
              ),
            },
            {
              id: 'actions',
              header: '',
              className: 'w-14',
              isActions: true,
              cell: (provider) =>
                canManage && provider.management_kind === 'tenant' ? (
                  <ResourceRowActions
                    onEdit={() => {
                      setEditProvider(provider)
                    }}
                    onDelete={() => {
                      if (!window.confirm(`Delete model provider ${provider.name}?`)) return
                      deleteProvider.mutate(provider.id, {
                        onError: (error) => {
                          window.alert(
                            error instanceof ApiError
                              ? error.message
                              : 'Could not delete model provider',
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
          getRowId={(provider) => provider.id}
          rowExpanded={(provider) => (
            <DetailList
              items={[
                { label: 'ID', value: provider.id, mono: true },
                {
                  label: 'Managed by',
                  value: provider.management_kind === 'cluster' ? 'Cluster' : 'Organization',
                },
                { label: 'API variant', value: provider.api_variant },
                { label: 'Endpoint path', value: provider.endpoint_path, mono: true },
                { label: 'Auth', value: provider.auth_kind },
                { label: 'Request timeout', value: `${provider.request_timeout_ms} ms` },
                { label: 'Created', value: formatDateTime(provider.created_at) },
                { label: 'Updated', value: formatDateTime(provider.updated_at) },
              ]}
            />
          )}
          isPending={query.isPending}
          isError={query.isError}
          onRetry={() => {
            void query.refetch()
          }}
          emptyMessage="No model providers yet. Connect OpenAI, OpenRouter, Anthropic, or Amazon Bedrock."
        />
      </div>
      {canManage && (
        <CreateModelProviderDialog open={open} onOpenChange={setOpen} orgId={activeOrg.id} />
      )}
      {canManage && editProvider && (
        <EditModelProviderDialog
          open
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setEditProvider(null)
          }}
          orgId={activeOrg.id}
          provider={editProvider}
        />
      )}
    </>
  )
}
