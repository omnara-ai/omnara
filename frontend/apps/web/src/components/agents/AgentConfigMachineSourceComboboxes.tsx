import { useMachinePool, useProjectMachinePoolGrants, useProjectMachines } from '@omnara/react'
import type { MachinePoolSummary, ProjectMachinePoolGrant } from '@omnara/sdk'
import { type ReactNode, useEffect, useState } from 'react'

import { PlusIcon } from '@/components/icons'
import { GrantMachinePoolDialog } from '@/components/projects/GrantMachinePoolDialog'
import { GrantProjectMachineDialog } from '@/components/projects/GrantProjectMachineDialog'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { exactNameGlob, useTypeaheadSearch } from '@/hooks/use-resource-list'
import { formatMemoryGb } from '@/lib/machine-memory'
import { useProjectPage } from '@/lib/use-project-page'
import { cn } from '@/lib/utils'

export interface SelectedPool {
  provider: string
  management_kind: string
}

function GrantAction({ label, onOpen }: { label: string; onOpen: () => void }): ReactNode {
  return (
    <button
      type="button"
      className="hover:bg-accent hover:text-accent-foreground flex w-full cursor-default items-center gap-2 rounded-sm py-2 pl-2 pr-8 text-sm outline-none"
      onClick={onOpen}
    >
      <PlusIcon className="size-4" />
      {label}
    </button>
  )
}

interface PoolOption {
  name: string
  pool?: SelectedPool
}

interface MachineOption {
  name: string
}

const PoolNameCombobox = createResourceCombobox<PoolOption>({
  itemKey: (item) => item.name,
  itemLabel: (item) => item.name,
  placeholder: 'Search machine pools…',
  emptyMessage: 'No machine pools granted.',
})

const MachineNameCombobox = createResourceCombobox<MachineOption>({
  itemKey: (machine) => machine.name,
  itemLabel: (machine) => machine.name,
  placeholder: 'Search machines…',
  emptyMessage: 'No machines granted.',
})

function useStaleName<TItem, TFetchResult>(
  lookupQuery: {
    data?: { pages: { data: TItem[] }[] }
    hasNextPage: boolean
    isError: boolean
    isFetching: boolean
    isPending: boolean
    fetchNextPage: () => Promise<TFetchResult>
  },
  enabled: boolean,
  value: string,
  matches: (item: TItem) => boolean,
  onUnavailableChange?: (unavailable: boolean) => void,
): TItem | undefined {
  const completeLookup = useCompleteInfiniteQueryItems(lookupQuery, enabled)
  const matchedItem = value === '' ? undefined : completeLookup.items.find(matches)
  const stale = value !== '' && completeLookup.isComplete && matchedItem === undefined
  useEffect(() => {
    onUnavailableChange?.(stale)
  }, [onUnavailableChange, stale])
  return matchedItem
}

export function PoolSourceCombobox({
  id,
  required,
  orgId,
  projectId,
  value,
  onChange,
  onUnavailableChange,
  onPoolResolved,
  onGrantResolved,
}: {
  id?: string
  required?: boolean
  orgId: string
  projectId: string
  value: string
  onChange: (name: string, pool?: SelectedPool) => void
  onUnavailableChange?: (unavailable: boolean) => void
  onPoolResolved?: (pool: SelectedPool) => void
  onGrantResolved?: (grant: ResolvedPoolGrant | null) => void
}) {
  const { project } = useProjectPage()
  const [grantOpen, setGrantOpen] = useState(false)
  const search = useTypeaheadSearch()
  const grantsQuery = useProjectMachinePoolGrants(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
  })
  const grants = useInfiniteQueryItems(grantsQuery)

  const listedItem = grants.find((item) => item.machine_pool.name === value)
  const listedNow = listedItem !== undefined
  const lookupQuery = useProjectMachinePoolGrants(orgId, projectId, {
    filters: { name: exactNameGlob(value) },
    pageSize: 1,
    enabled: value !== '' && !listedNow,
  })
  const lookedUpItem = useStaleName(
    lookupQuery,
    value !== '' && !listedNow,
    listedNow ? '' : value,
    (item) => item.machine_pool.name === value,
    onUnavailableChange,
  )
  const resolvedItem = listedItem ?? lookedUpItem
  const resolvedPool = resolvedItem?.machine_pool
  useEffect(() => {
    if (resolvedPool) onPoolResolved?.(resolvedPool)
  }, [onPoolResolved, resolvedPool])
  useEffect(() => {
    onGrantResolved?.(resolvedItem ?? null)
  }, [onGrantResolved, resolvedItem])

  return (
    <>
      <PoolNameCombobox
        id={id}
        required={required}
        items={grants.map((item) => ({
          name: item.machine_pool.name,
          pool: item.machine_pool,
        }))}
        value={value === '' ? null : { name: value }}
        onValueChange={(item) => {
          onChange(item?.name ?? '', item?.pool)
        }}
        search={search}
        query={grantsQuery}
        placeholder={grantsQuery.isPending ? 'Loading pools…' : 'Search machine pools…'}
        action={
          project?.access.can_manage_access && (
            <GrantAction
              label="Grant machine pool"
              onOpen={() => {
                setGrantOpen(true)
              }}
            />
          )
        }
      />
      {grantOpen && (
        <GrantMachinePoolDialog
          open
          onOpenChange={setGrantOpen}
          orgId={orgId}
          projectId={projectId}
          onGranted={(pool) => {
            onChange(pool.name, pool)
          }}
        />
      )}
    </>
  )
}

export function MachineSourceCombobox({
  id,
  required,
  orgId,
  projectId,
  value,
  onChange,
  onUnavailableChange,
  onMachinesGranted,
}: {
  id?: string
  required?: boolean
  orgId: string
  projectId: string
  value: string
  onChange: (name: string) => void
  onUnavailableChange?: (unavailable: boolean) => void
  onMachinesGranted?: (names: string[]) => void
}) {
  const { project } = useProjectPage()
  const [grantOpen, setGrantOpen] = useState(false)
  const search = useTypeaheadSearch()
  const machinesQuery = useProjectMachines(orgId, projectId, {
    filters: { source_kind: 'byo', ...search.filters },
    sort: 'name',
    pageSize: 25,
  })
  const machines = useInfiniteQueryItems(machinesQuery)

  const listedNow = machines.some((machine) => machine.display_name === value)
  const lookupQuery = useProjectMachines(orgId, projectId, {
    filters: { source_kind: 'byo', name: exactNameGlob(value) },
    pageSize: 1,
    enabled: value !== '' && !listedNow,
  })
  useStaleName(
    lookupQuery,
    value !== '' && !listedNow,
    listedNow ? '' : value,
    (machine) => machine.display_name === value,
    onUnavailableChange,
  )

  return (
    <>
      <MachineNameCombobox
        id={id}
        required={required}
        items={machines.map((machine) => ({ name: machine.display_name }))}
        value={value === '' ? null : { name: value }}
        onValueChange={(machine) => {
          onChange(machine?.name ?? '')
        }}
        search={search}
        query={machinesQuery}
        placeholder={machinesQuery.isPending ? 'Loading machines…' : 'Search machines…'}
        action={
          project?.access.can_manage_access && (
            <GrantAction
              label="Grant machine"
              onOpen={() => {
                setGrantOpen(true)
              }}
            />
          )
        }
      />
      {grantOpen && (
        <GrantProjectMachineDialog
          open
          onOpenChange={setGrantOpen}
          orgId={orgId}
          projectId={projectId}
          onGranted={(granted) => {
            const names = granted.map((machine) => machine.display_name)
            if (onMachinesGranted) {
              onMachinesGranted(names)
              return
            }
            const [first] = names
            if (first !== undefined) onChange(first)
          }}
        />
      )}
    </>
  )
}

export interface ResolvedPoolGrant {
  machine_pool: MachinePoolSummary
  grant: ProjectMachinePoolGrant
}

export function PoolGrantSummary({ machine_pool: pool, grant }: ResolvedPoolGrant) {
  const poolQuery = useMachinePool(pool.org_id, pool.id)
  const inherited = poolQuery.data
  const rows: { label: string; value: string | number | null | undefined; inherited?: boolean }[] =
    [
      resolve(
        'Memory',
        grant.default_machine_memory_mb,
        inherited?.default_machine_memory_mb,
        formatMemoryGb,
      ),
      resolve('CPU', grant.default_machine_cpu, inherited?.default_machine_cpu),
      resolve('Max machines', grant.max_total_machines, inherited?.max_total_machines),
      { label: 'Provider', value: pool.provider },
      { label: 'Management', value: pool.management_kind },
      resolve('Min machine CPU', grant.min_machine_cpu, inherited?.min_machine_cpu),
      resolve(
        'Min machine memory',
        grant.min_machine_memory_mb,
        inherited?.min_machine_memory_mb,
        formatMemoryGb,
      ),
      resolve('Max machine CPU', grant.max_machine_cpu, inherited?.max_machine_cpu),
      resolve(
        'Max machine memory',
        grant.max_machine_memory_mb,
        inherited?.max_machine_memory_mb,
        formatMemoryGb,
      ),
      resolve('Max total CPU', grant.max_total_cpu, inherited?.max_total_cpu),
      resolve(
        'Max total memory',
        grant.max_total_memory_mb,
        inherited?.max_total_memory_mb,
        formatMemoryGb,
      ),
    ]
  const half = Math.ceil(rows.length / 2)
  const columns = [rows.slice(0, half), rows.slice(half)]
  return (
    <div className="flex flex-col gap-2 text-xs">
      <p className="font-medium">{pool.name}</p>
      <div className="flex gap-6">
        {columns.map((column) => (
          <dl key={column[0]?.label} className="grid grid-cols-[auto_auto] gap-x-4 gap-y-0.5">
            {column.map((row) => {
              const empty = row.value == null || row.value === ''
              return (
                <div key={row.label} className="contents">
                  <dt className="text-muted-foreground">{row.label}</dt>
                  <dd
                    className={cn(
                      'text-right tabular-nums',
                      (empty || row.inherited) && 'text-muted-foreground',
                    )}
                  >
                    {empty ? (poolQuery.isPending ? '…' : 'Unset') : row.value}
                  </dd>
                </div>
              )
            })}
          </dl>
        ))}
      </div>
    </div>
  )
}

function resolve(
  label: string,
  own: number | null,
  inheritedValue: number | null | undefined,
  format: (value: number | null | undefined) => string | number | null | undefined = (value) =>
    value,
) {
  if (own != null) return { label, value: format(own) }
  return { label, value: format(inheritedValue), inherited: true }
}
