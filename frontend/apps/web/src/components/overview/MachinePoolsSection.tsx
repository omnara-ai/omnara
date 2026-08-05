import { type MachinePoolListSort, useDeleteMachinePool, useMachinePools } from '@omnara/react'
import { ApiError, type MachinePool } from '@omnara/sdk'
import { useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { CreateMachinePoolDialog } from '@/components/org/CreateMachinePoolDialog'
import { EditMachinePoolDialog } from '@/components/org/EditMachinePoolDialog'
import { GrantPoolToProjectDialog } from '@/components/org/GrantPoolToProjectDialog'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { formatDateTime } from '@/lib/format'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

type ActiveDialog =
  | { kind: 'create' }
  | { kind: 'edit'; pool: MachinePool }
  | { kind: 'grant'; pool: MachinePool }
  | null

export function MachinePoolsSection() {
  const { activeOrg } = useActiveOrg()
  const canManage = canManageOrg(activeOrg.role)
  const list = useResourceList<MachinePoolListSort>('-created_at')
  const query = useMachinePools(activeOrg.id, { filters: list.apiFilters, sort: list.sort })
  const paged = usePagedQuery(query, list.queryKey)
  const deletePool = useDeleteMachinePool(activeOrg.id)
  const [activeDialog, setActiveDialog] = useState<ActiveDialog>(null)

  const newPoolButton = () =>
    canManage ? (
      <Button
        size="sm"
        onClick={() => {
          setActiveDialog({ kind: 'create' })
        }}
      >
        New pool
      </Button>
    ) : undefined

  return (
    <>
      <div className="flex flex-col gap-3">
        <SearchHeader
          title="Machine pools"
          toolbar={
            <ResourceListToolbar
              search={list.search}
              onSearchChange={list.setSearch}
              sort={list.sort}
              sortOptions={resourceSortOptions}
              onSortChange={list.setSort}
              placeholder="Search pools by name…"
            />
          }
        >
          {newPoolButton()}
        </SearchHeader>
        <DataTable
          columns={[
            { header: 'Name' },
            { header: 'Provider' },
            { header: 'Resources', className: 'w-44' },
            { header: '', className: 'w-14', isActions: true },
          ]}
          data={paged.rows}
          isFiltered={list.isFiltering}
          pagination={paged.pagination}
          getRowId={(pool) => pool.id}
          rowCells={(pool) => [
            <span className="inline-flex max-w-full items-center gap-2">
              <span className="truncate font-medium">{pool.name}</span>
              {pool.management_kind === 'cluster' && <Badge variant="secondary">cluster</Badge>}
            </span>,
            pool.provider,
            machinePoolResources(pool),
            canManage ? (
              <ResourceRowActions
                onEdit={
                  pool.management_kind === 'tenant'
                    ? () => {
                        setActiveDialog({ kind: 'edit', pool })
                      }
                    : undefined
                }
                onGrant={() => {
                  setActiveDialog({ kind: 'grant', pool })
                }}
                onDelete={
                  pool.management_kind === 'tenant'
                    ? () => {
                        if (!window.confirm(`Delete machine pool ${pool.name}?`)) return
                        deletePool.mutate(pool.id, {
                          onError: (error) => {
                            window.alert(
                              error instanceof ApiError
                                ? error.message
                                : 'Could not delete machine pool',
                            )
                          },
                        })
                      }
                    : undefined
                }
              />
            ) : null,
          ]}
          rowExpanded={(pool) => (
            <DetailList
              items={[
                { label: 'ID', value: pool.id, mono: true },
                { label: 'Description', value: pool.description },
                {
                  label: 'Managed by',
                  value: pool.management_kind === 'cluster' ? 'Cluster' : 'Organization',
                },
                { label: 'Working directory', value: pool.default_cwd, mono: true },
                { label: 'Max machines', value: pool.max_total_machines },
                {
                  label: 'Max CPU',
                  value: pool.max_total_cpu ? `${pool.max_total_cpu} vCPU` : undefined,
                },
                {
                  label: 'Max memory',
                  value: pool.max_total_memory_mb ? `${pool.max_total_memory_mb} MB` : undefined,
                },
                { label: 'Created', value: formatDateTime(pool.created_at) },
                { label: 'Updated', value: formatDateTime(pool.updated_at) },
              ]}
            />
          )}
          isPending={query.isPending}
          isError={query.isError}
          onRetry={() => {
            void query.refetch()
          }}
          emptyMessage="No machine pools yet. Pools provision the machines your agents run on."
        />
      </div>
      {canManage && (
        <CreateMachinePoolDialog
          open={activeDialog?.kind === 'create'}
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setActiveDialog(null)
          }}
          orgId={activeOrg.id}
        />
      )}
      {canManage && activeDialog?.kind === 'edit' && (
        <EditMachinePoolDialog
          open
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setActiveDialog(null)
          }}
          orgId={activeOrg.id}
          pool={activeDialog.pool}
        />
      )}
      {canManage && activeDialog?.kind === 'grant' && (
        <GrantPoolToProjectDialog
          key={activeDialog.pool.id}
          open
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setActiveDialog(null)
          }}
          orgId={activeOrg.id}
          pool={activeDialog.pool}
        />
      )}
    </>
  )
}

function machinePoolResources(pool: MachinePool) {
  const resources: string[] = []
  if (pool.default_machine_cpu !== null) resources.push(`${pool.default_machine_cpu} vCPU`)
  if (pool.default_machine_memory_mb !== null) {
    resources.push(`${pool.default_machine_memory_mb} MB`)
  }
  return resources.join(' / ') || 'Provider-defined'
}
