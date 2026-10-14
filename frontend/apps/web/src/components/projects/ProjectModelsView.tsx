import {
  type ProjectModelGrantListSort,
  useClusterModelPricing,
  useDeleteProjectModelGrant,
  useProjectModelGrants,
} from '@omnara/react'
import { ApiError, type DiscoveredModelPricing, type ProjectModelGrantListItem } from '@omnara/sdk'
import { useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { ModelPricingSummary } from '@/components/models/ModelPricing'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { EditModelGrantDialog } from '@/components/projects/EditModelGrantDialog'
import { modelGrantOverrides } from '@/components/projects/grant-override-diffs'
import { GrantModelButton } from '@/components/projects/GrantModelButton'
import { OverrideChip, OverrideList } from '@/components/projects/GrantOverrides'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { createdResourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { guides } from '@/lib/docs'
import { formatDateTime } from '@/lib/format'
import { modelPricingDetailItems } from '@/lib/model-pricing'

/**
 * Models shared with the project, one page at a time. Anyone who can read the project can
 * view them; sharing, editing and revoking grants requires project access management.
 */
export function ProjectModelsView({
  orgId,
  projectId,
  canManageAccess,
}: {
  orgId: string
  projectId: string
  canManageAccess: boolean
}) {
  const list = useResourceList<ProjectModelGrantListSort>('-created_at')
  const grantsQuery = useProjectModelGrants(orgId, projectId, {
    filters: list.apiFilters,
    sort: list.sort,
  })
  const paged = usePagedQuery(grantsQuery, list.queryKey)
  const deleteGrant = useDeleteProjectModelGrant(orgId, projectId)
  const pricing = useClusterModelPricing(orgId)
  const [editing, setEditing] = useState<ProjectModelGrantListItem | null>(null)

  function stopSharing(item: ProjectModelGrantListItem) {
    if (!window.confirm(`Stop sharing ${item.model.name} with this project?`)) return
    deleteGrant.mutate(item.grant.id, {
      onError: (error) => {
        window.alert(error instanceof ApiError ? error.message : 'Could not stop sharing model')
      },
    })
  }

  return (
    <div className="flex flex-col gap-3">
      <SearchHeader
        title="Shared models"
        description="Models agents in this project can use"
        guide={guides.modelProviders}
        toolbar={
          <ResourceListToolbar
            search={list.search}
            onSearchChange={list.setSearch}
            sort={{ value: list.sort, options: createdResourceSortOptions, onChange: list.setSort }}
            placeholder="Search shared models by name…"
            showSearch
          />
        }
      >
        <GrantModelButton />
      </SearchHeader>
      <DataTable
        columns={[
          {
            id: 'model',
            header: 'Model',
            cell: (item) => (
              <span className="inline-flex max-w-full items-center gap-2">
                <span className="truncate font-medium">{item.model.name}</span>
                <OverrideChip count={modelGrantOverrides(item.grant, undefined).length} />
              </span>
            ),
          },
          {
            id: 'provider',
            header: 'Provider',
            cell: (item) => (
              <span className="text-muted-foreground">{item.model.provider_config || '—'}</span>
            ),
          },
          {
            id: 'pricing',
            header: 'Price / 1M',
            cell: (item) => (
              <ModelPricingSummary
                className="text-muted-foreground whitespace-nowrap tabular-nums"
                pricing={pricing.pricingFor(
                  item.model.model_provider_config_id,
                  item.model.provider_model_slug,
                )}
              />
            ),
          },
          {
            id: 'actions',
            header: '',
            className: 'w-14',
            isActions: true,
            cell: (item) => (
              <ResourceRowActions
                deleteLabel="Stop sharing"
                onEdit={
                  canManageAccess
                    ? () => {
                        setEditing(item)
                      }
                    : undefined
                }
                onDelete={
                  canManageAccess
                    ? () => {
                        stopSharing(item)
                      }
                    : undefined
                }
              />
            ),
          },
        ]}
        data={paged.rows}
        isFiltered={list.isFiltering}
        pagination={paged.pagination}
        getRowId={(item) => item.grant.id}
        rowExpanded={(item) => (
          <SharedModelDetails
            item={item}
            pricing={pricing.pricingFor(
              item.model.model_provider_config_id,
              item.model.provider_model_slug,
            )}
          />
        )}
        isPending={grantsQuery.isPending}
        isError={grantsQuery.isError}
        onRetry={() => {
          void grantsQuery.refetch()
        }}
        emptyMessage={
          canManageAccess
            ? 'No shared models. Share an organization model so agents in this project can use it.'
            : 'No models are shared with this project yet. Ask a project admin to share one.'
        }
        emptyAction={<GrantModelButton />}
      />
      {canManageAccess && editing && (
        <EditModelGrantDialog
          key={editing.grant.id}
          open
          onOpenChange={(open) => {
            if (!open) setEditing(null)
          }}
          orgId={orgId}
          projectId={projectId}
          item={editing}
        />
      )}
    </div>
  )
}

function SharedModelDetails({
  item,
  pricing,
}: {
  item: ProjectModelGrantListItem
  pricing: DiscoveredModelPricing | undefined
}) {
  const overrides = modelGrantOverrides(item.grant, undefined)
  return (
    <div className="flex flex-col gap-4">
      {overrides.length > 0 && <OverrideList overrides={overrides} />}
      <DetailList
        items={[
          { label: 'ID', value: item.grant.id, mono: true },
          { label: 'Configured model', value: item.grant.configured_model_id, mono: true },
          { label: 'Provider model', value: item.model.provider_model_slug, mono: true },
          ...modelPricingDetailItems(pricing),
          { label: 'Shared', value: formatDateTime(item.grant.created_at) },
        ]}
      />
    </div>
  )
}
