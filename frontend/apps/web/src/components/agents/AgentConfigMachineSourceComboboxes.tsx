import { useProjectMachinePoolGrants, useProjectMachines } from '@omnara/react'
import type { ProjectMachinePoolGrantListItem, VisibleMachine } from '@omnara/sdk'
import { useEffect } from 'react'

import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { exactNameGlob, useTypeaheadSearch } from '@/hooks/use-resource-list'

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
 * Clear a selected source name once its point lookup confirms the resource is
 * gone. Pass an empty value to skip the check (e.g. when the current list
 * already contains the name).
 */
function useResetStaleName<TItem>(
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
  onChange: (name: string) => void,
) {
  const completeLookup = useCompleteInfiniteQueryItems(lookupQuery, enabled)
  const stale = value !== '' && completeLookup.isComplete && !completeLookup.items.some(matches)
  useEffect(() => {
    if (stale) onChange('')
  }, [onChange, stale])
}

export function PoolSourceCombobox({
  orgId,
  projectId,
  value,
  onChange,
}: {
  orgId: string
  projectId: string
  value: string
  onChange: (name: string, item?: ProjectMachinePoolGrantListItem) => void
}) {
  const search = useTypeaheadSearch()
  const grantsQuery = useProjectMachinePoolGrants(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
  })
  const grants = useInfiniteQueryItems(grantsQuery)

  const listedNow = grants.some((item) => item.machine_pool.name === value)
  const lookupQuery = useProjectMachinePoolGrants(orgId, projectId, {
    filters: { name: exactNameGlob(value) },
    pageSize: 1,
    enabled: value !== '' && !listedNow,
  })
  useResetStaleName(
    lookupQuery,
    value !== '' && !listedNow,
    listedNow ? '' : value,
    (item) => item.machine_pool.name === value,
    onChange,
  )

  return (
    <PoolNameCombobox
      items={grants}
      value={value || null}
      onValueChange={(item) => {
        onChange(item?.machine_pool.name ?? '', item ?? undefined)
      }}
      search={search}
      query={grantsQuery}
      placeholder={grantsQuery.isPending ? 'Loading pools…' : 'Search machine pools…'}
    />
  )
}

export function MachineSourceCombobox({
  orgId,
  projectId,
  value,
  onChange,
}: {
  orgId: string
  projectId: string
  value: string
  onChange: (name: string) => void
}) {
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
  useResetStaleName(
    lookupQuery,
    value !== '' && !listedNow,
    listedNow ? '' : value,
    (machine) => machine.display_name === value,
    onChange,
  )

  return (
    <MachineNameCombobox
      items={machines}
      value={value || null}
      onValueChange={(machine) => {
        onChange(machine?.display_name ?? '')
      }}
      search={search}
      query={machinesQuery}
      placeholder={machinesQuery.isPending ? 'Loading machines…' : 'Search machines…'}
    />
  )
}
