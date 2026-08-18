import { useCreateProject } from '@omnara/react'
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
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError } from '@/lib/submit-status'

interface NewProjectState {
  name: string
  status: SubmitStatus
}

export function NewProjectDialog({
  open,
  onOpenChange,
  orgId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
}) {
  const createProject = useCreateProject(orgId)
  const [state, setState] = useState<NewProjectState>({ name: '', status: idle })
  const errorMessage = statusError(state.status)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setState((prev) => ({ ...prev, status: idle }))
    try {
      await createProject.mutateAsync({ name: state.name })
      setState((prev) => ({ ...prev, name: '' }))
      onOpenChange(false)
    } catch (err) {
      const status = submitError(err, 'Could not create project')
      setState((prev) => ({
        ...prev,
        status,
      }))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New project</DialogTitle>
          <DialogDescription>Group agents and runs under a project.</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            void submit(event)
          }}
        >
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="new-project-name">Name</FieldLabel>
              <Input
                id="new-project-name"
                required
                value={state.name}
                placeholder="Production"
                onChange={(event) => {
                  setState((prev) => ({ ...prev, name: event.target.value }))
                }}
              />
            </Field>
            {errorMessage && <p className="text-destructive text-sm">{errorMessage}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={createProject.isPending || state.name.trim() === ''}
                loading={createProject.isPending}
              >
                Create project
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
