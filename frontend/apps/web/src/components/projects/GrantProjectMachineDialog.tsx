import { useGrantMachineToProject, useMachines, useProjectMachineGrants } from '@omnara/react'
import { type VisibleMachine } from '@omnara/sdk'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { createResourceMultiCombobox } from '@/components/ui/resource-combobox'
import { Spinner } from '@/components/ui/spinner'
import { useBatchGrantSubmit } from '@/hooks/use-batch-grant-submit'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'

const MachineMultiCombobox = createResourceMultiCombobox<VisibleMachine>({
  itemKey: (machine) => machine.id,
  itemLabel: (machine) => machine.display_name,
  renderItem: (machine) => (
    <span className="flex min-w-0 flex-col">
      <span className="truncate">{machine.display_name}</span>
      <span className="text-muted-foreground text-xs">{machine.provider}</span>
    </span>
  ),
  placeholder: 'Search machines…',
  emptyMessage: 'No ungranted machines found.',
})

export function GrantProjectMachineDialog({
  open,
  onOpenChange,
  orgId,
  projectId,
  onGranted,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  projectId: string
  onGranted?: (machines: VisibleMachine[]) => void
}) {
  const mutation = useGrantMachineToProject(orgId)
  const batch = useBatchGrantSubmit<VisibleMachine>({
    label: 'machine grant',
    fallbackError: 'Could not grant machines',
    itemKey: (machine) => machine.id,
    grant: (machine) => mutation.mutateAsync({ projectID: projectId, machineID: machine.id }),
    onGranted,
    onSuccess: () => {
      onOpenChange(false)
    },
  })
  const search = useTypeaheadSearch()
  const machinesQuery = useMachines(orgId, {
    filters: { source_kind: 'byo', ...search.filters },
    sort: 'name',
    pageSize: 25,
    enabled: open,
  })
  const grantsQuery = useProjectMachineGrants(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
    enabled: open,
  })
  const completeGrants = useCompleteInfiniteQueryItems(grantsQuery, open)
  const grantedIds = new Set(completeGrants.items.map((item) => item.grant.machine_id))
  const machines = useInfiniteQueryItems(machinesQuery).filter(
    (machine) =>
      machine.source_kind === 'byo' && machine.access.can_manage && !grantedIds.has(machine.id),
  )
  const queryError = machinesQuery.isError || grantsQuery.isError

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Grant machines</DialogTitle>
          <DialogDescription>
            Let this project run agents on an organization BYO machine.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={(event) => void batch.submit(event)}>
          <FieldGroup>
            <Field>
              <FieldLabel>Machines</FieldLabel>
              <MachineMultiCombobox
                items={machines}
                value={batch.items.map((machine) => machine.id)}
                onValueChange={batch.setItems}
                search={search}
                query={machinesQuery}
                pending={machinesQuery.isPending || completeGrants.isPending}
                disabled={batch.isSubmitting || queryError || !completeGrants.isComplete}
              />
              {!queryError && !machinesQuery.isPending && machines.length === 0 && (
                <FieldDescription>
                  Every organization BYO machine is already granted, or none exist yet.
                </FieldDescription>
              )}
            </Field>
            {queryError && (
              <p className="text-destructive text-sm">
                Could not load grantable machines.{' '}
                <button
                  type="button"
                  className="underline"
                  onClick={() => {
                    void Promise.all([machinesQuery.refetch(), grantsQuery.refetch()])
                  }}
                >
                  Retry
                </button>
              </p>
            )}
            {batch.errorMessage && <p className="text-destructive text-sm">{batch.errorMessage}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={
                  batch.isSubmitting ||
                  batch.items.length === 0 ||
                  queryError ||
                  !completeGrants.isComplete
                }
              >
                {batch.isSubmitting && <Spinner />}Grant machines
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
