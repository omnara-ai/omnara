import { type MachineListSort, useMachines, useProjectMachines } from '@omnara/react'
import type { MachineSourceKind, VisibleMachine } from '@omnara/sdk'
import type { ReactNode } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { MachineConnectionDot } from '@/components/overview/MachineConnectionDot'
import { machineDetailItems, machineStatusLabel } from '@/components/overview/machineFormat'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import type { Guide } from '@/lib/docs'
import { formatDateTime, formatTimeAgo } from '@/lib/format'

export interface MachineActions {
  onGrant: (machine: VisibleMachine) => void
  onDelete: (machine: VisibleMachine) => void
}

/**
 * Searchable, paginated list of machines narrowed by `filters` (a pool, or BYO): the org's
 * inventory, or with `projectId` the machines visible to that project.
 */
export function MachinesTable({
  orgId,
  projectId,
  filters,
  description,
  guide,
  headerAction,
  actions,
  emptyMessage,
  emptyAction,
}: {
  orgId: string
  projectId?: string
  filters: { source_kind?: MachineSourceKind; machine_pool_id?: string }
  description: string
  guide?: Guide
  headerAction?: ReactNode
  /** Share/delete handlers; offered only on BYO machines the caller can manage. */
  actions?: MachineActions
  emptyMessage: string
  emptyAction?: ReactNode
}) {
  const list = useResourceList<MachineListSort>('-updated_at')
  const options = { filters: { ...list.apiFilters, ...filters }, sort: list.sort }
  const orgQuery = useMachines(orgId, { ...options, enabled: projectId === undefined })
  const projectQuery = useProjectMachines(orgId, projectId ?? '', {
    ...options,
    enabled: projectId !== undefined,
  })
  const query = projectId === undefined ? orgQuery : projectQuery
  const paged = usePagedQuery(query, list.queryKey)

  return (
    <div className="flex flex-col gap-3">
      <SearchHeader
        title="Machines"
        description={description}
        guide={guide}
        toolbar={
          <ResourceListToolbar
            search={list.search}
            onSearchChange={list.setSearch}
            sort={{ value: list.sort, options: resourceSortOptions, onChange: list.setSort }}
            placeholder="Search machines by name…"
            showSearch
          />
        }
      >
        {headerAction}
      </SearchHeader>
      <DataTable
        columns={[
          {
            id: 'name',
            header: 'Name',
            cell: (machine) => (
              <span className="inline-flex max-w-full items-center gap-2">
                <MachineConnectionDot state={machine.connection_state} />
                <span className="truncate font-medium">{machine.display_name}</span>
              </span>
            ),
          },
          {
            id: 'status',
            header: 'Status',
            className: 'w-36',
            cell: (machine) => <span className="capitalize">{machineStatusLabel(machine)}</span>,
          },
          {
            id: 'last-seen',
            header: 'Last seen',
            className: 'w-36',
            cell: (machine) => {
              const lastSeen = machine.last_observed_at ?? machine.updated_at
              return (
                <span
                  className="text-muted-foreground tabular-nums"
                  title={formatDateTime(lastSeen)}
                >
                  {formatTimeAgo(lastSeen)}
                </span>
              )
            },
          },
          ...(actions
            ? [
                {
                  id: 'actions',
                  header: '',
                  className: 'w-14',
                  isActions: true,
                  cell: (machine: VisibleMachine) =>
                    machine.source_kind === 'byo' && machine.access.can_manage ? (
                      <ResourceRowActions
                        onGrant={() => {
                          actions.onGrant(machine)
                        }}
                        onDelete={() => {
                          actions.onDelete(machine)
                        }}
                      />
                    ) : null,
                },
              ]
            : []),
        ]}
        data={paged.rows}
        isFiltered={list.isFiltering}
        pagination={paged.pagination}
        getRowId={(machine) => machine.id}
        rowExpanded={(machine) => <DetailList items={machineDetailItems(machine)} />}
        isPending={query.isPending}
        isError={query.isError}
        onRetry={() => {
          void query.refetch()
        }}
        emptyMessage={emptyMessage}
        emptyAction={emptyAction}
      />
    </div>
  )
}
