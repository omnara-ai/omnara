import {
  useCreateProjectMachinePoolGrant,
  useMachinePools,
  useProjectMachinePoolGrants,
} from '@omnara/react'
import { type MachinePool } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

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
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, submitting } from '@/lib/submit-status'

import {
  emptyPoolGrantDraft,
  poolGrantCreateRequest,
  type PoolGrantOverrideDraft,
  poolGrantOverridesValid,
} from './GrantMachinePoolDialogState'
import { PoolGrantOverridesCollapsible } from './GrantMachinePoolOverridesSection'

const MachinePoolCombobox = createResourceCombobox<MachinePool>({
  itemKey: (pool) => pool.id,
  itemLabel: (pool) => pool.name,
  placeholder: 'Search machine pools…',
  emptyMessage: 'No ungranted pools found.',
})

interface SelectedPool {
  pool: MachinePool
  draft: PoolGrantOverrideDraft
}

export function GrantMachinePoolDialog({
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
  onGranted?: (pool: MachinePool) => void
}) {
  const createGrant = useCreateProjectMachinePoolGrant(orgId, projectId)
  const [selected, setSelected] = useState<SelectedPool | null>(null)
  const [status, setStatus] = useState<SubmitStatus>(idle)
  const poolsQuery = useMachinePools(orgId, { enabled: open })
  const grantsQuery = useProjectMachinePoolGrants(orgId, projectId, { enabled: open })
  const completeGrants = useCompleteInfiniteQueryItems(grantsQuery, open)
  const grantedPoolIds = new Set(completeGrants.items.map((item) => item.grant.machine_pool_id))
  const pools = useInfiniteQueryItems(poolsQuery).filter((pool) => !grantedPoolIds.has(pool.id))
  const queryError = poolsQuery.isError || grantsQuery.isError
  const isSubmitting = status.phase === 'submitting'
  const errorMessage = statusError(status)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!selected) return
    setStatus(submitting)
    try {
      await createGrant.mutateAsync(poolGrantCreateRequest(selected.pool, selected.draft))
    } catch (err) {
      setStatus(submitError(err, 'Could not grant machine pool'))
      return
    }
    setStatus(idle)
    setSelected(null)
    onGranted?.(selected.pool)
    onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85svh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Grant machine pool</DialogTitle>
          <DialogDescription>
            Let agents in this project run on an organization machine pool.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={(event) => void submit(event)}>
          <FieldGroup>
            <Field>
              <FieldLabel>Machine pool</FieldLabel>
              <MachinePoolCombobox
                items={pools}
                value={selected?.pool ?? null}
                onValueChange={(pool) => {
                  setSelected(pool ? { pool, draft: emptyPoolGrantDraft() } : null)
                }}
                query={poolsQuery}
                pending={poolsQuery.isPending || completeGrants.isPending}
                disabled={isSubmitting || queryError || !completeGrants.isComplete}
              />
              {!queryError &&
                !poolsQuery.isPending &&
                completeGrants.isComplete &&
                pools.length === 0 && (
                  <FieldDescription>
                    Every organization machine pool is already granted, or none exist yet.
                  </FieldDescription>
                )}
            </Field>
            {selected && (
              <PoolGrantOverridesCollapsible
                orgId={orgId}
                projectId={projectId}
                enabled={open}
                idPrefix={`${selected.pool.id}-grant`}
                pool={selected.pool}
                values={selected.draft}
                onChange={(draft) => {
                  setSelected({ pool: selected.pool, draft })
                }}
              />
            )}
            {queryError && (
              <p className="text-destructive text-sm">
                Could not load grantable machine pools.{' '}
                <button
                  type="button"
                  className="underline"
                  onClick={() => {
                    void Promise.all([poolsQuery.refetch(), grantsQuery.refetch()])
                  }}
                >
                  Retry
                </button>
              </p>
            )}
            {errorMessage && <p className="text-destructive text-sm">{errorMessage}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={
                  isSubmitting ||
                  !selected ||
                  queryError ||
                  !completeGrants.isComplete ||
                  !poolGrantOverridesValid(selected.draft)
                }
                loading={isSubmitting}
              >
                Grant pool
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
