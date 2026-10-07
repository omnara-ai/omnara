import {
  type ProjectMachineGrantListSort,
  type ProjectMachinePoolGrantListSort,
  useDeleteProjectMachineGrant,
  useDeleteProjectMachinePoolGrant,
  useProjectMachineGrants,
  useProjectMachinePoolGrants,
} from '@omnara/react'
import { type ProjectMachinePoolGrantListItem } from '@omnara/sdk'
import { useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { EditMachinePoolGrantDialog } from '@/components/projects/EditMachinePoolGrantDialog'
import { GrantMachineButton } from '@/components/projects/GrantMachineButton'
import { GrantMachinePoolButton } from '@/components/projects/GrantMachinePoolButton'
import { usePagedQuery } from '@/hooks/use-paged-query'
import {
  createdResourceSortOptions,
  resourceSortOptions,
  useListToolbarVisibility,
  useResourceList,
} from '@/hooks/use-resource-list'
import { guides } from '@/lib/docs'
import { formatDateTime } from '@/lib/format'
import { formatMemoryGb } from '@/lib/machine-memory'
import { canManageMachineGrants } from '@/lib/permissions'
import { type ProviderOptions, providerOptionSummaries } from '@/lib/provider-options'
import { useActiveOrg } from '@/lib/use-active-org'

function overlaySummary(overlay: ProviderOptions) {
  const summary = Object.entries(providerOptionSummaries(overlay))
    .map(([key, text]) => {
      return text.length > 60 || text.includes('\n')
        ? `${key}: (${text.length} chars)`
        : `${key}: ${text}`
    })
    .join('\n')
  return summary === '' ? '' : <span className="whitespace-pre-line">{summary}</span>
}

function envOverlaySummary(overlay: Record<string, string | null>) {
  const summary = Object.entries(overlay)
    .map(([key, value]) => (value === null ? `${key}: (unset)` : `${key}: ${value}`))
    .join('\n')
  return summary === '' ? '' : <span className="whitespace-pre-line">{summary}</span>
}

/**
 * Machine pools and machines shared with the project. Anyone who can read the
 * project can view them; editing and revoking grants requires project access
 * management plus org management (the shared machines belong to the org).
 */
export function ProjectMachineGrantsTables({
  orgId,
  projectId,
  canManageAccess,
}: {
  orgId: string
  projectId: string
  canManageAccess: boolean
}) {
  const poolList = useResourceList<ProjectMachinePoolGrantListSort>('-created_at')
  const grantsQuery = useProjectMachinePoolGrants(orgId, projectId, {
    filters: poolList.apiFilters,
    sort: poolList.sort,
  })
  const grantsPaged = usePagedQuery(grantsQuery, poolList.queryKey)
  const poolToolbarVisible = useListToolbarVisibility(
    poolList,
    grantsPaged.pagination,
    grantsQuery.isSuccess,
  )
  const machineList = useResourceList<ProjectMachineGrantListSort>('-updated_at')
  const machineGrantsQuery = useProjectMachineGrants(orgId, projectId, {
    filters: machineList.apiFilters,
    sort: machineList.sort,
  })
  const machineGrantsPaged = usePagedQuery(machineGrantsQuery, machineList.queryKey)
  const machineToolbarVisible = useListToolbarVisibility(
    machineList,
    machineGrantsPaged.pagination,
    machineGrantsQuery.isSuccess,
  )
  const deleteGrant = useDeleteProjectMachinePoolGrant(orgId, projectId)
  const deleteMachineGrant = useDeleteProjectMachineGrant(orgId, projectId)
  const [editing, setEditing] = useState<ProjectMachinePoolGrantListItem | null>(null)
  const { activeOrg } = useActiveOrg()
  const canManageGrants = canManageMachineGrants(activeOrg.role, {
    can_manage_access: canManageAccess,
  })

  return (
    <>
      <div className="flex flex-col gap-3">
        <SearchHeader
          title="Shared machine pools"
          description="List of machine pools accessible to agents in your current project"
          guide={guides.machinePools}
          toolbar={
            <ResourceListToolbar
              search={poolList.search}
              onSearchChange={poolList.setSearch}
              sort={{
                value: poolList.sort,
                options: createdResourceSortOptions,
                onChange: poolList.setSort,
              }}
              placeholder="Search shared pools by name…"
              showSearch={poolToolbarVisible}
            />
          }
        >
          <GrantMachinePoolButton />
        </SearchHeader>
        <DataTable
          columns={[
            {
              id: 'pool',
              header: 'Pool',
              cell: (item) => <span className="font-medium">{item.machine_pool.name}</span>,
            },
            {
              id: 'description',
              header: 'Description',
              cell: (item) => (
                <span className="text-muted-foreground">{item.grant.description || '—'}</span>
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
                    canManageGrants
                      ? () => {
                          setEditing(item)
                        }
                      : undefined
                  }
                  onDelete={
                    canManageGrants
                      ? () => {
                          if (!window.confirm('Stop sharing this machine pool with the project?'))
                            return
                          deleteGrant.mutate(item.grant.id)
                        }
                      : undefined
                  }
                />
              ),
            },
          ]}
          data={grantsPaged.rows}
          isFiltered={poolList.isFiltering}
          pagination={grantsPaged.pagination}
          getRowId={(item) => item.grant.id}
          rowExpanded={(item) => (
            <DetailList
              items={[
                { label: 'ID', value: item.grant.id, mono: true },
                { label: 'Machine pool', value: item.grant.machine_pool_id, mono: true },
                { label: 'Working directory', value: item.grant.default_cwd, mono: true },
                { label: 'Machine CPU', value: item.grant.default_machine_cpu },
                {
                  label: 'Machine memory',
                  value: formatMemoryGb(item.grant.default_machine_memory_mb),
                },
                {
                  label: 'Provider options',
                  value: overlaySummary(item.grant.default_machine_provider_options_overlay),
                  mono: true,
                },
                {
                  label: 'Environment overlay',
                  value: envOverlaySummary(item.grant.default_machine_env_overlay),
                  mono: true,
                },
                {
                  label: 'Secret environment overlay',
                  value: envOverlaySummary(item.grant.default_machine_secret_env_overlay),
                  mono: true,
                },
                { label: 'Max machines', value: item.grant.max_total_machines },
                { label: 'Max total CPU', value: item.grant.max_total_cpu },
                {
                  label: 'Max total memory',
                  value: formatMemoryGb(item.grant.max_total_memory_mb),
                },
                { label: 'Min machine CPU', value: item.grant.min_machine_cpu },
                {
                  label: 'Min machine memory',
                  value: formatMemoryGb(item.grant.min_machine_memory_mb),
                },
                { label: 'Max machine CPU', value: item.grant.max_machine_cpu },
                {
                  label: 'Max machine memory',
                  value: formatMemoryGb(item.grant.max_machine_memory_mb),
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
            canManageGrants
              ? 'No shared machine pools. Share a pool so agents in this project can run.'
              : 'No machine pools are shared with this project yet.'
          }
          emptyAction={<GrantMachinePoolButton />}
        />
        {canManageGrants && editing && (
          <EditMachinePoolGrantDialog
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
      <div className="flex flex-col gap-3">
        <SearchHeader
          title="Shared machines"
          description="List of machines accessible to agents in your current project"
          guide={guides.machines}
          toolbar={
            <ResourceListToolbar
              search={machineList.search}
              onSearchChange={machineList.setSearch}
              sort={{
                value: machineList.sort,
                options: resourceSortOptions,
                onChange: machineList.setSort,
              }}
              placeholder="Search shared machines by name…"
              showSearch={machineToolbarVisible}
            />
          }
        >
          <GrantMachineButton />
        </SearchHeader>
        <DataTable
          columns={[
            {
              id: 'machine',
              header: 'Machine',
              cell: (item) => <span className="font-medium">{item.machine.display_name}</span>,
            },
            {
              id: 'description',
              header: 'Description',
              cell: (item) => (
                <span className="text-muted-foreground">{item.grant.description || '—'}</span>
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
                  onDelete={
                    canManageGrants
                      ? () => {
                          if (!window.confirm('Stop sharing this machine with the project?')) return
                          deleteMachineGrant.mutate(item.grant.id)
                        }
                      : undefined
                  }
                />
              ),
            },
          ]}
          data={machineGrantsPaged.rows}
          isFiltered={machineList.isFiltering}
          pagination={machineGrantsPaged.pagination}
          getRowId={(item) => item.grant.id}
          rowExpanded={(item) => (
            <DetailList
              items={[
                { label: 'ID', value: item.grant.id, mono: true },
                { label: 'Machine', value: item.grant.machine_id, mono: true },
                { label: 'Source', value: item.grant.source_kind },
                { label: 'Created', value: formatDateTime(item.grant.created_at) },
              ]}
            />
          )}
          isPending={machineGrantsQuery.isPending}
          isError={machineGrantsQuery.isError}
          onRetry={() => {
            void machineGrantsQuery.refetch()
          }}
          emptyMessage="No individual machines shared with this project."
          emptyAction={<GrantMachineButton />}
        />
      </div>
    </>
  )
}
