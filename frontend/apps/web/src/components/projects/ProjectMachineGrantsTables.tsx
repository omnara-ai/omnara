import {
  type ProjectMachineGrantListSort,
  type ProjectMachinePoolGrantListSort,
  useDeleteProjectMachineGrant,
  useDeleteProjectMachinePoolGrant,
  useProjectMachineGrants,
  useProjectMachinePoolGrants,
} from '@omnara/react'
import { type ProjectMachinePoolGrantListItem } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { EditMachinePoolGrantDialog } from '@/components/projects/EditMachinePoolGrantDialog'
import { GrantMachineButton } from '@/components/projects/GrantMachineButton'
import { GrantMachinePoolButton } from '@/components/projects/GrantMachinePoolButton'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import {
  createdResourceSortOptions,
  resourceSortOptions,
  useResourceList,
} from '@/hooks/use-resource-list'
import { formatDateTime } from '@/lib/format'
import { formatMemoryGb } from '@/lib/machine-memory'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

function overlaySummary(overlay: Record<string, unknown>) {
  const summary = Object.entries(overlay)
    .map(([key, value]) => {
      const text = typeof value === 'string' ? value : JSON.stringify(value)
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

export function ProjectMachineGrantsTables({
  orgId,
  projectId,
}: {
  orgId: string
  projectId: string
}) {
  const poolList = useResourceList<ProjectMachinePoolGrantListSort>('-created_at')
  const grantsQuery = useProjectMachinePoolGrants(orgId, projectId, {
    filters: poolList.apiFilters,
    sort: poolList.sort,
  })
  const grantsPaged = usePagedQuery(grantsQuery, poolList.queryKey)
  const machineList = useResourceList<ProjectMachineGrantListSort>('-updated_at')
  const machineGrantsQuery = useProjectMachineGrants(orgId, projectId, {
    filters: machineList.apiFilters,
    sort: machineList.sort,
  })
  const machineGrantsPaged = usePagedQuery(machineGrantsQuery, machineList.queryKey)
  const deleteGrant = useDeleteProjectMachinePoolGrant(orgId, projectId)
  const deleteMachineGrant = useDeleteProjectMachineGrant(orgId, projectId)
  const [editing, setEditing] = useState<ProjectMachinePoolGrantListItem | null>(null)
  const { activeOrg } = useActiveOrg()
  const canDeleteGrants = canManageOrg(activeOrg.role)

  return (
    <>
      <div className="flex flex-col gap-3">
        <SearchHeader
          title="Machine pool grants"
          toolbar={
            <ResourceListToolbar
              search={poolList.search}
              onSearchChange={poolList.setSearch}
              sort={poolList.sort}
              sortOptions={createdResourceSortOptions}
              onSortChange={poolList.setSort}
              placeholder="Search pool grants by name…"
            />
          }
        >
          {
            <>
              <Button asChild size="sm" variant="ghost">
                <Link to="/machines">Organization machines</Link>
              </Button>
              <GrantMachinePoolButton />
            </>
          }
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
                  deleteLabel="Delete grant"
                  onEdit={() => {
                    setEditing(item)
                  }}
                  onDelete={
                    canDeleteGrants
                      ? () => {
                          if (!window.confirm('Delete this machine pool grant?')) return
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
          emptyMessage="No machine pools granted. Grant a pool so agents in this project can run."
        />
        {editing && (
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
          title="Machine grants"
          toolbar={
            <ResourceListToolbar
              search={machineList.search}
              onSearchChange={machineList.setSearch}
              sort={machineList.sort}
              sortOptions={resourceSortOptions}
              onSortChange={machineList.setSort}
              placeholder="Search machine grants by name…"
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
                  deleteLabel="Delete grant"
                  onDelete={
                    canDeleteGrants
                      ? () => {
                          if (!window.confirm('Delete this machine grant?')) return
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
          emptyMessage="No individual machines granted to this project."
        />
      </div>
    </>
  )
}
