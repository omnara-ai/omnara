import { useProjectMachinePoolGrants, useProjectMachines } from '@omnara/react'
import type { ProjectMachinePoolGrantListItem, VisibleMachine } from '@omnara/sdk'
import { PlusIcon } from 'lucide-react'
import { type ReactNode, useEffect, useState } from 'react'

import { GrantMachinePoolDialog } from '@/components/projects/GrantMachinePoolDialog'
import { GrantProjectMachineDialog } from '@/components/projects/GrantProjectMachineDialog'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { exactNameGlob, useTypeaheadSearch } from '@/hooks/use-resource-list'
import { useProjectPage } from '@/lib/use-project-page'

/** Pool fields the machine sources form needs from a selected pool. */
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

const PoolNameCombobox = createResourceCombobox<ProjectMachinePoolGrantListItem>({
  itemKey: (item) => item.machine_pool.name,
  itemLabel: (item) => item.machine_pool.name,
  placeholder: 'Search machine pools…',
  emptyMessage: 'No machine pools granted.',
})

const MachineNameCombobox = createResourceCombobox<VisibleMachine>({
  itemKey: (machine) => machine.display_name,
  itemLabel: (machine) => machine.display_name,
  placeholder: 'Search machines…',
  emptyMessage: 'No machines granted.',
})

/**
 * Report whether the selected source name no longer resolves in the project,
 * once its point lookup completes. The name is kept so an existing config
 * stays editable; callers surface the problem instead. Pass an empty value to
 * skip the check (e.g. when the current list already contains the name).
 * Returns the matched item once the lookup finds it.
 */
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
  orgId,
  projectId,
  value,
  onChange,
  onUnavailableChange,
  onPoolResolved,
}: {
  orgId: string
  projectId: string
  value: string
  onChange: (name: string, pool?: SelectedPool) => void
  onUnavailableChange?: (unavailable: boolean) => void
  /** Reports the granted pool the current value resolves to, e.g. so callers can backfill pool fields a deserialized config lacks. */
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
        items={grants}
        value={value || null}
        onValueChange={(item) => {
          onChange(item?.machine_pool.name ?? '', item?.machine_pool)
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
  orgId,
  projectId,
  value,
  onChange,
  onUnavailableChange,
}: {
  orgId: string
  projectId: string
  value: string
  onChange: (name: string) => void
  onUnavailableChange?: (unavailable: boolean) => void
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
        items={machines}
        value={value || null}
        onValueChange={(machine) => {
          onChange(machine?.display_name ?? '')
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
            const [first] = granted
            if (first) onChange(first.display_name)
          }}
        />
      )}
    </>
  )
}
