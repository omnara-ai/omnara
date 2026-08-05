import { useUpdateMachinePool } from '@omnara/react'
import { type MachinePool } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError } from '@/lib/submit-status'

interface EditMachinePoolState {
  name: string
  description: string
  maxMachines: string
  status: SubmitStatus
}

export function EditMachinePoolDialog({
  open,
  onOpenChange,
  orgId,
  pool,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  pool: MachinePool
}) {
  const mutation = useUpdateMachinePool(orgId)
  const [state, setState] = useState<EditMachinePoolState>({
    name: pool.name,
    description: pool.description,
    maxMachines: String(pool.max_total_machines),
    status: idle,
  })
  const errorMessage = statusError(state.status)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setState((prev) => ({ ...prev, status: idle }))
    try {
      await mutation.mutateAsync({
        poolID: pool.id,
        name: state.name.trim(),
        description: state.description.trim(),
        max_total_machines: Number(state.maxMachines),
      })
      onOpenChange(false)
    } catch (err) {
      setState((prev) => ({
        ...prev,
        status: submitError(err, 'Could not update machine pool'),
      }))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Edit machine pool</DialogTitle>
        </DialogHeader>
        <form onSubmit={(event) => void submit(event)}>
          <FieldGroup>
            <Field>
              <FieldLabel>Name</FieldLabel>
              <Input
                value={state.name}
                onChange={(event) => {
                  setState((prev) => ({ ...prev, name: event.target.value }))
                }}
              />
            </Field>
            <Field>
              <FieldLabel>Description</FieldLabel>
              <Input
                value={state.description}
                onChange={(event) => {
                  setState((prev) => ({ ...prev, description: event.target.value }))
                }}
              />
            </Field>
            <Field>
              <FieldLabel>Maximum machines</FieldLabel>
              <Input
                type="number"
                min="0"
                value={state.maxMachines}
                onChange={(event) => {
                  setState((prev) => ({ ...prev, maxMachines: event.target.value }))
                }}
              />
            </Field>
            {errorMessage && <p className="text-destructive text-sm">{errorMessage}</p>}
            <DialogFooter>
              <Button type="submit" disabled={mutation.isPending || state.name.trim() === ''}>
                {mutation.isPending && <Spinner />}Save changes
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
