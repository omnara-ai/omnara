import { useProjectMachinePoolGrants, useProjectMachines } from '@omnara/react'

import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'

interface PoolSourceSelection {
  name: string
  provider: string
  managementKind: string
}

interface MachineSourceSelection {
  name: string
}

const PoolNameCombobox = createResourceCombobox<PoolSourceSelection>({
  itemKey: (pool) => pool.name,
  itemLabel: (pool) => pool.name,
  placeholder: 'Search machine pools…',
  emptyMessage: 'No machine pools granted.',
})

const MachineNameCombobox = createResourceCombobox<MachineSourceSelection>({
  itemKey: (machine) => machine.name,
  itemLabel: (machine) => machine.name,
  placeholder: 'Search machines…',
  emptyMessage: 'No machines granted.',
})

export function PoolSourceCombobox({
  orgId,
  projectId,
  value,
  onChange,
}: {
  orgId: string
  projectId: string
  value: PoolSourceSelection | null
  onChange: (selection: PoolSourceSelection | null) => void
}) {
  const search = useTypeaheadSearch()
  const grantsQuery = useProjectMachinePoolGrants(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
  })
  const grants = useInfiniteQueryItems(grantsQuery).map(({ machine_pool: pool }) => ({
    name: pool.name,
    provider: pool.provider,
    managementKind: pool.management_kind,
  }))

  return (
    <PoolNameCombobox
      items={grants}
      value={value}
      onValueChange={onChange}
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
  value: MachineSourceSelection | null
  onChange: (selection: MachineSourceSelection | null) => void
}) {
  const search = useTypeaheadSearch()
  const machinesQuery = useProjectMachines(orgId, projectId, {
    filters: { source_kind: 'byo', ...search.filters },
    sort: 'name',
    pageSize: 25,
  })
  const machines = useInfiniteQueryItems(machinesQuery).map((machine) => ({
    name: machine.display_name,
  }))

  return (
    <MachineNameCombobox
      items={machines}
      value={value}
      onValueChange={onChange}
      search={search}
      query={machinesQuery}
      placeholder={machinesQuery.isPending ? 'Loading machines…' : 'Search machines…'}
    />
  )
}
