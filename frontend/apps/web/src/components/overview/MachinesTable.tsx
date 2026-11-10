import { type MachineListSort, useMachines, useProjectMachines } from '@omnara/react'
import type { MachineSourceKind, VisibleMachine } from '@omnara/sdk'
import { type ReactNode, useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { MachineSettingsDialog } from '@/components/machines/MachineSettingsDialog'
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
  settings = false,
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
  settings?: boolean
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
  const [settingsTarget, setSettingsTarget] = useState<VisibleMachine | null>(null)

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
          ...(actions || settings
            ? [
                {
                  id: 'actions',
                  header: '',
                  className: 'w-14',
                  isActions: true,
                  cell: (machine: VisibleMachine) => {
                    if (!machine.access.can_manage) return null
                    const byoActions = actions && machine.source_kind === 'byo' ? actions : null
                    return (
                      <ResourceRowActions
                        editLabel="Settings"
                        onEdit={
                          settings
                            ? () => {
                                setSettingsTarget(machine)
                              }
                            : undefined
                        }
                        onGrant={
                          byoActions
                            ? () => {
                                byoActions.onGrant(machine)
                              }
                            : undefined
                        }
                        onDelete={
                          byoActions
                            ? () => {
                                byoActions.onDelete(machine)
                              }
                            : undefined
                        }
                      />
                    )
                  },
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
      {settingsTarget && (
        <MachineSettingsDialog
          open
          onOpenChange={(open) => {
            if (!open) setSettingsTarget(null)
          }}
          orgId={orgId}
          machineId={settingsTarget.id}
          machineName={settingsTarget.display_name}
        />
      )}
    </div>
  )
}
