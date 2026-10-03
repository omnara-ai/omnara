import {
  type ProjectModelGrantListSort,
  useClusterModelPricing,
  useDeleteProjectModelGrant,
  useProjectModelGrants,
} from '@omnara/react'
import { type ProjectModelGrantListItem } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { ModelPricingSummary } from '@/components/models/ModelPricing'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { EditModelGrantDialog } from '@/components/projects/EditModelGrantDialog'
import { GrantModelButton } from '@/components/projects/GrantModelButton'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import {
  createdResourceSortOptions,
  useListToolbarVisibility,
  useResourceList,
} from '@/hooks/use-resource-list'
import { guides } from '@/lib/docs'
import { formatDateTime } from '@/lib/format'
import { modelPricingDetailItems } from '@/lib/model-pricing'

/**
 * Models shared with the project. Anyone who can read the project can view the
 * list; editing and revoking grants requires project access management.
 */
export function ProjectModelGrantsTable({
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
  const grantsPaged = usePagedQuery(grantsQuery, list.queryKey)
  const showToolbar = useListToolbarVisibility(list, grantsPaged.pagination, grantsQuery.isSuccess)
  const deleteGrant = useDeleteProjectModelGrant(orgId, projectId)
  const pricing = useClusterModelPricing(orgId)
  const [editing, setEditing] = useState<ProjectModelGrantListItem | null>(null)

  return (
    <div className="flex flex-col gap-3">
      <SearchHeader
        title="Shared models"
        description="List of models accessible to your current project"
        guide={guides.modelProviders}
        toolbar={
          <ResourceListToolbar
            search={list.search}
            onSearchChange={list.setSearch}
            sort={{ value: list.sort, options: createdResourceSortOptions, onChange: list.setSort }}
            placeholder="Search shared models by name…"
            showSearch={showToolbar}
          />
        }
      >
        <Button asChild size="sm" variant="ghost">
          <Link to="/models" hash="model-providers">
            Providers
          </Link>
        </Button>
        <Button asChild size="sm" variant="ghost">
          <Link to="/models" hash="configured-models">
            Organization models
          </Link>
        </Button>
        <GrantModelButton />
      </SearchHeader>
      <DataTable
        columns={[
          {
            id: 'model',
            header: 'Model',
            cell: (item) => <span className="font-medium">{item.model.name}</span>,
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
                        if (!window.confirm('Stop sharing this model with the project?')) return
                        deleteGrant.mutate(item.grant.id)
                      }
                    : undefined
                }
              />
            ),
          },
        ]}
        data={grantsPaged.rows}
        isFiltered={list.isFiltering}
        pagination={grantsPaged.pagination}
        getRowId={(item) => item.grant.id}
        rowExpanded={(item) => (
          <DetailList
            items={[
              { label: 'ID', value: item.grant.id, mono: true },
              { label: 'Configured model', value: item.grant.configured_model_id, mono: true },
              { label: 'Provider model', value: item.model.provider_model_slug, mono: true },
              ...modelPricingDetailItems(
                pricing.pricingFor(
                  item.model.model_provider_config_id,
                  item.model.provider_model_slug,
                ),
              ),
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
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setEditing(null)
          }}
          orgId={orgId}
          projectId={projectId}
          item={editing}
        />
      )}
    </div>
  )
}
