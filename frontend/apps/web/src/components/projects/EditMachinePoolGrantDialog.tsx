import { useMachinePool, useUpdateProjectMachinePoolGrant } from '@omnara/react'
import { type ProjectMachinePoolGrantListItem } from '@omnara/sdk'
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
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, submitting } from '@/lib/submit-status'

import {
  poolGrantDraftFromGrant,
  type PoolGrantOverrideDraft,
  poolGrantOverridesValid,
  poolGrantUpdateRequest,
} from './GrantMachinePoolDialogState'
import { PoolGrantOverrideFields } from './GrantMachinePoolOverridesSection'

export function EditMachinePoolGrantDialog({
  open,
  onOpenChange,
  orgId,
  projectId,
  item,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  projectId: string
  item: ProjectMachinePoolGrantListItem
}) {
  const updateGrant = useUpdateProjectMachinePoolGrant(orgId, projectId)
  const poolQuery = useMachinePool(orgId, item.grant.machine_pool_id, { enabled: open })
  const [draft, setDraft] = useState<PoolGrantOverrideDraft>(() => poolGrantDraftFromGrant(item))
  const [status, setStatus] = useState<SubmitStatus>(idle)
  const pool = poolQuery.data
  const isSubmitting = status.phase === 'submitting'
  const errorMessage = statusError(status)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!pool) return
    setStatus(submitting)
    try {
      await updateGrant.mutateAsync({
        poolGrantID: item.grant.id,
        ...poolGrantUpdateRequest(pool, item.grant, draft),
      })
    } catch (err) {
      setStatus(submitError(err, 'Could not update machine pool grant'))
      return
    }
    setStatus(idle)
    onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85svh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Edit pool grant</DialogTitle>
          <DialogDescription>
            Overrides for the {item.machine_pool.name} pool in this project.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={(event) => void submit(event)}>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor={`${item.grant.id}-edit-description`}>Description</FieldLabel>
              <Input
                id={`${item.grant.id}-edit-description`}
                value={draft.description}
                onChange={(event) => {
                  setDraft({ ...draft, description: event.target.value })
                }}
              />
            </Field>
            {pool && (
              <PoolGrantOverrideFields
                orgId={orgId}
                projectId={projectId}
                enabled={open}
                idPrefix={`${item.grant.id}-edit`}
                pool={pool}
                values={draft}
                onChange={setDraft}
              />
            )}
            {!pool && poolQuery.isPending && (
              <div className="flex justify-center py-4">
                <Spinner />
              </div>
            )}
            {poolQuery.isError && (
              <p className="text-destructive text-sm">
                Could not load the machine pool.{' '}
                <button
                  type="button"
                  className="underline"
                  onClick={() => {
                    void poolQuery.refetch()
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
                disabled={isSubmitting || !pool || !poolGrantOverridesValid(draft)}
              >
                {isSubmitting && <Spinner />}
                Save changes
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
