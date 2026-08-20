import { type MachinePoolListSort, useDeleteMachinePool, useMachinePools } from '@omnara/react'
import { ApiError, type MachinePool } from '@omnara/sdk'
import { useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { type DetailItem, DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { CreateMachinePoolDialog } from '@/components/org/CreateMachinePoolDialog'
import { EditMachinePoolDialog } from '@/components/org/EditMachinePoolDialog'
import { GrantPoolToProjectDialog } from '@/components/org/GrantPoolToProjectDialog'
import {
  isMachinePoolProvider,
  machinePoolProviderDefinitions,
} from '@/components/org/machinePoolProviders'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { formatDateTime } from '@/lib/format'
import { formatMemoryGb } from '@/lib/machine-memory'
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
            {
              id: 'name',
              header: 'Name',
              cell: (pool) => (
                <span className="inline-flex max-w-full items-center gap-2">
                  <span className="truncate font-medium">{pool.name}</span>
                  {pool.management_kind === 'cluster' && (
                    <span className="text-muted-foreground">cluster</span>
                  )}
                </span>
              ),
            },
            { id: 'provider', header: 'Provider', cell: (pool) => pool.provider },
            {
              id: 'machines-in-use',
              header: 'Machines in use',
              className: 'w-44',
              cell: (pool) =>
                pool.usage ? `${pool.usage.machines} / ${pool.max_total_machines}` : '—',
            },
            {
              id: 'resources-in-use',
              header: 'Resources in use',
              className: 'w-44',
              cell: formatResourceUsage,
            },
            {
              id: 'actions',
              header: '',
              className: 'w-14',
              isActions: true,
              cell: (pool) =>
                canManage ? (
                  <ResourceRowActions
                    onEdit={() => {
                      setActiveDialog({ kind: 'edit', pool })
                    }}
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
            },
          ]}
          data={paged.rows}
          isFiltered={list.isFiltering}
          pagination={paged.pagination}
          getRowId={(pool) => pool.id}
          rowExpanded={(pool) => (
            <DetailList
              items={[
                { label: 'ID', value: pool.id, mono: true },
                { label: 'Description', value: pool.description },
                ...providerDetails(pool),
                ...(pool.provider_auth_secret_id
                  ? [
                      {
                        label: 'Provider credential',
                        value: pool.provider_auth_secret_id,
                        mono: true,
                      },
                    ]
                  : []),
                {
                  label: 'Startup script',
                  value: (
                    <span className="whitespace-pre-wrap">
                      {stringValue(pool.default_machine_provider_options.startup_script) ?? 'None'}
                    </span>
                  ),
                  mono: true,
                },
                {
                  label: 'Runtime protection',
                  value: pool.runtime_protection_enabled ? 'Enabled' : 'Disabled',
                },
                { label: 'Working directory', value: pool.default_cwd, mono: true },
                {
                  label: 'Environment variables',
                  value: formatEntries(pool.default_machine_env),
                  mono: true,
                },
                {
                  label: 'Secret variables',
                  value: formatEntries(pool.default_machine_secret_env),
                  mono: true,
                },
                { label: 'Machine quota', value: pool.max_total_machines },
                {
                  label: 'CPU quota',
                  value: formatCPU(pool.max_total_cpu),
                },
                {
                  label: 'Memory quota',
                  value: formatMemoryGb(pool.max_total_memory_mb),
                },
                {
                  label: 'Default CPU per machine',
                  value: formatCPU(pool.default_machine_cpu),
                },
                {
                  label: 'Min CPU per machine',
                  value: formatCPU(pool.min_machine_cpu),
                },
                {
                  label: 'Max CPU per machine',
                  value: formatCPU(pool.max_machine_cpu),
                },
                {
                  label: 'Default memory per machine',
                  value: formatMemoryGb(pool.default_machine_memory_mb),
                },
                {
                  label: 'Min memory per machine',
                  value: formatMemoryGb(pool.min_machine_memory_mb),
                },
                {
                  label: 'Max memory per machine',
                  value: formatMemoryGb(pool.max_machine_memory_mb),
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
          key={activeDialog.pool.id}
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

function formatCPU(value: number | null) {
  return value === null ? undefined : `${value} vCPU`
}

function formatResourceUsage(pool: MachinePool) {
  const values: string[] = []
  if (pool.max_total_cpu !== null) {
    values.push(`${pool.usage?.cpu ?? '—'} / ${pool.max_total_cpu} vCPU`)
  }
  if (pool.max_total_memory_mb !== null) {
    values.push(
      `${formatMemoryGb(pool.usage?.memory_mb) ?? '—'} / ${formatMemoryGb(pool.max_total_memory_mb)}`,
    )
  }
  if (values.length === 0) return '—'
  return (
    <span className="flex flex-col whitespace-nowrap">
      {values.map((value) => (
        <span key={value}>{value}</span>
      ))}
    </span>
  )
}

function providerDetails(pool: MachinePool): DetailItem[] {
  if (!isMachinePoolProvider(pool.provider)) return []
  const definition = machinePoolProviderDefinitions[pool.provider]
  const options = pool.default_machine_provider_options
  const details: DetailItem[] = [
    {
      label: `${definition.label} ${definition.resource.label.toLowerCase()}`,
      value: stringValue(options[definition.resource.key]),
      mono: true,
    },
    {
      label: `${definition.label} ${definition.location.label.toLowerCase()}`,
      value: stringValue(options[definition.location.key]),
    },
  ]
  if (definition.requiresWorkspace) {
    details.push({
      label: `${definition.label} workspace`,
      value: stringValue(pool.provider_config.workspace),
    })
  }
  return details
}

function stringValue(value: unknown) {
  return typeof value === 'string' ? value : undefined
}

function formatEntries(values: Record<string, string>) {
  const entries = Object.entries(values).sort(([left], [right]) => left.localeCompare(right))
  if (entries.length === 0) return 'None'
  return (
    <span className="whitespace-pre-wrap break-all">
      {entries.map(([key, value]) => `${key}=${value}`).join('\n')}
    </span>
  )
}
