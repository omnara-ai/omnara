import { useProjectMachinePoolGrants, useProjectMachines } from '@omnara/react'
import { type ReactNode, useEffect, useState } from 'react'

import { PlusIcon } from '@/components/icons'
import { GrantMachinePoolDialog } from '@/components/projects/GrantMachinePoolDialog'
import { GrantProjectMachineDialog } from '@/components/projects/GrantProjectMachineDialog'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { exactNameGlob, useTypeaheadSearch } from '@/hooks/use-resource-list'
import { useProjectPage } from '@/lib/use-project-page'

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

function useStaleName<TItem>(
  lookupQuery: {
    data?: { pages: { data: TItem[] }[] }
    hasNextPage: boolean
    isError: boolean
    isFetching: boolean
    isPending: boolean
    fetchNextPage: () => Promise<unknown>
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
}: {
  id?: string
  required?: boolean
  orgId: string
  projectId: string
  value: string
  onChange: (name: string, pool?: SelectedPool) => void
  onUnavailableChange?: (unavailable: boolean) => void
  onPoolResolved?: (pool: SelectedPool) => void
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
  const resolvedPool = (listedItem ?? lookedUpItem)?.machine_pool
  useEffect(() => {
    if (resolvedPool) onPoolResolved?.(resolvedPool)
  }, [onPoolResolved, resolvedPool])

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
