import { useCreateOrganization } from '@omnara/react'
import { useNavigate } from '@tanstack/react-router'
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
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import { Spinner } from '@/components/ui/spinner'
import { resourceNameInputMaxLength, resourceNameValid } from '@/lib/resource-name'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError } from '@/lib/submit-status'
import { useActiveOrg } from '@/lib/use-active-org'

interface CreateOrgState {
  name: string
  idempotencyKey: string
  status: SubmitStatus
}

function initialState(): CreateOrgState {
  return { name: '', idempotencyKey: crypto.randomUUID(), status: idle }
}

export function CreateOrgDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const createOrganization = useCreateOrganization()
  const navigate = useNavigate()
  const { setActiveOrgId } = useActiveOrg()
  const [state, setState] = useState<CreateOrgState>(initialState)
  const errorMessage = statusError(state.status)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setState((prev) => ({ ...prev, status: idle }))
    try {
      const result = await createOrganization.mutateAsync({
        body: { name: state.name },
        idempotencyKey: state.idempotencyKey,
      })
      setActiveOrgId(result.org.id)
      await navigate({ to: '/', replace: true })
      setState(initialState())
      onOpenChange(false)
    } catch (err) {
      setState((prev) => ({
        ...prev,
        status: submitError(err, 'Could not create organization'),
      }))
    }
  }

  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) {
      setState(initialState())
    }
    onOpenChange(nextOpen)
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create organization</DialogTitle>
          <DialogDescription>
            You become the owner and can invite teammates later.
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            void submit(event)
          }}
        >
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="create-org-name">Name</FieldLabel>
              <Input
                id="create-org-name"
                required
                maxLength={resourceNameInputMaxLength}
                value={state.name}
                placeholder="Acme Inc."
                autoComplete="organization"
                onChange={(event) => {
                  setState((prev) => ({
                    ...prev,
                    name: event.target.value,
                    idempotencyKey: crypto.randomUUID(),
                  }))
                }}
              />
              <ResourceNameFieldError value={state.name} />
            </Field>
            {errorMessage && <p className="text-destructive text-sm">{errorMessage}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={createOrganization.isPending || !resourceNameValid(state.name)}
              >
                {createOrganization.isPending && <Spinner />}
                Create organization
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
