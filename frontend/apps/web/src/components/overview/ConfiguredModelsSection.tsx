import {
  useConfiguredModelOptions,
  useDeleteConfiguredModel,
  useModelProviders,
} from '@omnara/react'
import { ApiError, type ConfiguredModel } from '@omnara/sdk'
import { useEffect, useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { CreateConfiguredModelDialog } from '@/components/org/CreateConfiguredModelDialog'
import { EditConfiguredModelDialog } from '@/components/org/EditConfiguredModelDialog'
import { GrantConfiguredModelDialog } from '@/components/org/GrantConfiguredModelDialog'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useArrayPagination } from '@/hooks/use-array-pagination'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import {
  filterAndSortLocalItems,
  resourceSortOptions,
  useResourceList,
} from '@/hooks/use-resource-list'
import { formatDateTime } from '@/lib/format'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

type ActiveDialog =
  | { kind: 'create' }
  | { kind: 'edit'; model: ConfiguredModel }
  | { kind: 'grant'; model: ConfiguredModel }
  | null

export function ConfiguredModelsSection() {
  const { activeOrg } = useActiveOrg()
  const canManage = canManageOrg(activeOrg.role)
  const providersQuery = useModelProviders(activeOrg.id)
  const loadedProviders = useInfiniteQueryItems(providersQuery)
  const {
    fetchNextPage: fetchNextProviderPage,
    hasNextPage: hasNextProviderPage,
    isError: providersError,
    isFetchingNextPage: isFetchingNextProviderPage,
  } = providersQuery

  // Configured models are nested below provider configs in the API. This
  // inventory must resolve every provider page before its model aggregation
  // and create-dialog choices can be treated as complete.
  useEffect(() => {
    if (!hasNextProviderPage || isFetchingNextProviderPage || providersError) return
    void fetchNextProviderPage()
  }, [fetchNextProviderPage, hasNextProviderPage, isFetchingNextProviderPage, providersError])

  const providersPending = providersQuery.isPending || (hasNextProviderPage && !providersError)
  const providers = providersPending || providersError ? [] : loadedProviders
  const tenantProviders = providers.filter((provider) => provider.management_kind === 'tenant')
  const modelsQuery = useConfiguredModelOptions(activeOrg.id, providers)
  const deleteModel = useDeleteConfiguredModel(activeOrg.id)
  const [activeDialog, setActiveDialog] = useState<ActiveDialog>(null)
  const list = useResourceList<string>('-created_at')
  // Newest first for the overview; the hook's name ordering is for pickers.
  const models = [...(modelsQuery.data ?? [])].sort((left, right) => {
    const createdAt = Date.parse(right.model.created_at) - Date.parse(left.model.created_at)
    return createdAt || right.model.id.localeCompare(left.model.id)
  })

  const newModelButton = () =>
    canManage && tenantProviders.length > 0 ? (
      <Button
        size="sm"
        onClick={() => {
          setActiveDialog({ kind: 'create' })
        }}
      >
        New model
      </Button>
    ) : undefined

  const filteredModels = filterAndSortLocalItems(models, list, {
    name: (option) => option.model.name,
    provider: (option) => option.provider.name,
    created_at: (option) => option.model.created_at,
    updated_at: (option) => option.model.updated_at,
  })
  const paged = useArrayPagination(filteredModels, (option) => option.model.id)

  return (
    <>
      <div className="flex flex-col gap-3">
        <SearchHeader
          title="Configured models"
          toolbar={
            <ResourceListToolbar
              search={list.search}
              onSearchChange={list.setSearch}
              sort={list.sort}
              sortOptions={resourceSortOptions}
              onSortChange={list.setSort}
              placeholder="Search models by name…"
            />
          }
        >
          {newModelButton()}
        </SearchHeader>
        <DataTable
          columns={[
            {
              id: 'name',
              header: 'Name',
              cell: (option) => (
                <span className="inline-flex max-w-full items-center gap-2">
                  <span className="truncate font-medium">{option.model.name}</span>
                  {option.provider.management_kind === 'cluster' && (
                    <Badge variant="secondary">cluster</Badge>
                  )}
                </span>
              ),
            },
            { id: 'provider', header: 'Provider', cell: (option) => option.provider.name },
            {
              id: 'model',
              header: 'Model',
              cell: (option) => (
                <span className="text-muted-foreground">{option.model.provider_model_slug}</span>
              ),
            },
            {
              id: 'actions',
              header: '',
              className: 'w-14',
              isActions: true,
              cell: (option) =>
                canManage ? (
                  <ResourceRowActions
                    onEdit={
                      option.provider.management_kind === 'tenant'
                        ? () => {
                            setActiveDialog({ kind: 'edit', model: option.model })
                          }
                        : undefined
                    }
                    onGrant={() => {
                      setActiveDialog({ kind: 'grant', model: option.model })
                    }}
                    onDelete={
                      option.provider.management_kind === 'tenant'
                        ? () => {
                            if (!window.confirm(`Delete configured model ${option.model.name}?`))
                              return
                            deleteModel.mutate(
                              {
                                modelProviderConfigID: option.provider.id,
                                configuredModelID: option.model.id,
                              },
                              {
                                onError: (error) => {
                                  window.alert(
                                    error instanceof ApiError
                                      ? error.message
                                      : 'Could not delete configured model',
                                  )
                                },
                              },
                            )
                          }
                        : undefined
                    }
                  />
                ) : null,
            },
          ]}
          data={paged.rows}
          isFiltered={list.isFiltering}
          pagination={paged.pagination}
          getRowId={(option) => option.model.id}
          rowExpanded={({ model }) => (
            <DetailList
              items={[
                { label: 'ID', value: model.id, mono: true },
                { label: 'Provider model', value: model.provider_model_slug, mono: true },
                {
                  label: 'Context window',
                  value: `${model.context_window_tokens.toLocaleString()} tokens`,
                },
                {
                  label: 'Max output',
                  value: model.max_output_tokens
                    ? `${model.max_output_tokens.toLocaleString()} tokens`
                    : undefined,
                },
                {
                  label: 'Default max output',
                  value: model.default_max_output_tokens
                    ? `${model.default_max_output_tokens.toLocaleString()} tokens`
                    : undefined,
                },
                { label: 'Tools', value: model.supports_tools ? 'Supported' : 'Not supported' },
                { label: 'Created', value: formatDateTime(model.created_at) },
                { label: 'Updated', value: formatDateTime(model.updated_at) },
              ]}
            />
          )}
          isPending={providersPending || (providers.length > 0 && modelsQuery.isPending)}
          isError={providersQuery.isError || modelsQuery.isError}
          onRetry={() => {
            void Promise.all([providersQuery.refetch(), modelsQuery.refetch()])
          }}
          emptyMessage={
            providers.length === 0
              ? 'No model providers yet. Create a provider before adding configured models.'
              : 'No configured models yet.'
          }
        />
      </div>
      {canManage && tenantProviders.length > 0 && (
        <CreateConfiguredModelDialog
          open={activeDialog?.kind === 'create'}
          onOpenChange={(open) => {
            if (!open) setActiveDialog(null)
          }}
          orgId={activeOrg.id}
          providers={tenantProviders}
        />
      )}
      {canManage && activeDialog?.kind === 'grant' && (
        <GrantConfiguredModelDialog
          open
          onOpenChange={(open) => {
            if (!open) {
              setActiveDialog(null)
            }
          }}
          orgId={activeOrg.id}
          model={activeDialog.model}
        />
      )}
      {canManage && activeDialog?.kind === 'edit' && (
        <EditConfiguredModelDialog
          open
          onOpenChange={(open) => {
            if (!open) setActiveDialog(null)
          }}
          orgId={activeOrg.id}
          model={activeDialog.model}
        />
      )}
    </>
  )
}
