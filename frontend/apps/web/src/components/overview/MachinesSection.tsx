import {
  type MachineListSort,
  useDeleteMachine,
  useGrantMachineToProject,
  useMachines,
} from '@omnara/react'
import { ApiError, type VisibleMachine } from '@omnara/sdk'
import { useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { ConnectMachineDialog } from '@/components/org/ConnectMachineDialog'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { GrantToProjectDialog } from '@/components/projects/GrantToProjectDialog'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { formatDateTime } from '@/lib/format'
import { useActiveOrg } from '@/lib/use-active-org'

export function MachinesSection() {
  const { activeOrg } = useActiveOrg()
  const list = useResourceList<MachineListSort>('-updated_at')
  const query = useMachines(activeOrg.id, {
    filters: list.apiFilters,
    sort: list.sort,
  })
  const paged = usePagedQuery(query, list.queryKey)
  const deleteMachine = useDeleteMachine(activeOrg.id)
  const grantMachineMutation = useGrantMachineToProject(activeOrg.id)
  const [connectOpen, setConnectOpen] = useState(false)
  const [grantMachine, setGrantMachine] = useState<VisibleMachine | null>(null)

  const connectButton = () => (
    <Button
      size="sm"
      onClick={() => {
        setConnectOpen(true)
      }}
    >
      Connect machine
    </Button>
  )

  return (
    <>
      <div className="flex flex-col gap-3">
        <SearchHeader
          title="Machines"
          toolbar={
            <ResourceListToolbar
              search={list.search}
              onSearchChange={list.setSearch}
              sort={list.sort}
              sortOptions={resourceSortOptions}
              onSortChange={list.setSort}
              placeholder="Search machines by name…"
            />
          }
        >
          {connectButton()}
        </SearchHeader>
        <DataTable
          columns={[
            {
              id: 'name',
              header: 'Name',
              cell: (machine) => <span className="font-medium">{machine.display_name}</span>,
            },
            { id: 'provider', header: 'Provider', cell: (machine) => machine.provider },
            {
              id: 'state',
              header: 'State',
              className: 'w-36',
              cell: (machine) => <span className="capitalize">{machine.lifecycle_state}</span>,
            },
            {
              id: 'connection',
              header: 'Connection',
              className: 'w-36',
              cell: (machine) => <span className="capitalize">{machine.connection_state}</span>,
            },
            {
              id: 'actions',
              header: '',
              className: 'w-14',
              isActions: true,
              cell: (machine) =>
                machine.source_kind === 'byo' && machine.access.can_manage ? (
                  <ResourceRowActions
                    onGrant={() => {
                      setGrantMachine(machine)
                    }}
                    onDelete={() => {
                      if (!window.confirm(`Delete machine ${machine.display_name}?`)) return
                      deleteMachine.mutate(machine.id, {
                        onError: (error) => {
                          window.alert(
                            error instanceof ApiError ? error.message : 'Could not delete machine',
                          )
                        },
                      })
                    }}
                  />
                ) : null,
            },
          ]}
          data={paged.rows}
          isFiltered={list.isFiltering}
          pagination={paged.pagination}
          getRowId={(machine) => machine.id}
          rowExpanded={(machine) => (
            <DetailList
              items={[
                { label: 'ID', value: machine.id, mono: true },
                { label: 'Description', value: machine.description },
                { label: 'Source', value: machine.source_kind },
                { label: 'Last observed', value: formatDateTime(machine.last_observed_at) },
                { label: 'Created', value: formatDateTime(machine.created_at) },
                { label: 'Updated', value: formatDateTime(machine.updated_at) },
              ]}
            />
          )}
          isPending={query.isPending}
          isError={query.isError}
          onRetry={() => {
            void query.refetch()
          }}
          emptyMessage="No machines yet. Connect one you operate, or let pools provision them."
        />
      </div>
      <ConnectMachineDialog open={connectOpen} onOpenChange={setConnectOpen} orgId={activeOrg.id} />
      {grantMachine?.source_kind === 'byo' && (
        <GrantToProjectDialog
          open
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setGrantMachine(null)
          }}
          orgId={activeOrg.id}
          resourceName={grantMachine.display_name}
          isProjectEligible={(project) => project.access.can_manage_access}
          onGrant={(projectID) =>
            grantMachineMutation.mutateAsync({ projectID, machineID: grantMachine.id })
          }
        />
      )}
    </>
  )
}
