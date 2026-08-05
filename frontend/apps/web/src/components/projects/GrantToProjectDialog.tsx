import type { VisibleProject } from '@omnara/sdk'
import { type ReactNode, type SyntheticEvent, useState } from 'react'

import { ProjectGrantsField } from '@/components/projects/ProjectGrantsField'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { FieldGroup } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'
import { collectGrantFailures } from '@/lib/grant-failures'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, submitting } from '@/lib/submit-status'

interface GrantToProjectState {
  projectIds: string[]
  status: SubmitStatus
}

export function GrantToProjectDialog({
  open,
  onOpenChange,
  orgId,
  resourceName,
  onGrant,
  isProjectEligible,
  excludedProjectIds = [],
  options,
  submitDisabled = false,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  resourceName: string
  onGrant: (projectId: string) => Promise<unknown>
  isProjectEligible: (project: VisibleProject) => boolean
  excludedProjectIds?: string[]
  options?: ReactNode
  submitDisabled?: boolean
}) {
  const [state, setState] = useState<GrantToProjectState>({
    projectIds: [],
    status: idle,
  })
  const isSubmitting = state.status.phase === 'submitting'
  const errorMessage = statusError(state.status)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setState((prev) => ({ ...prev, status: submitting }))
    try {
      const results = await Promise.allSettled(state.projectIds.map(onGrant))
      const failures = collectGrantFailures(state.projectIds, results)
      if (failures) {
        setState((prev) => ({
          ...prev,
          projectIds: failures.failedProjectIds,
          status: { phase: 'error', message: failures.message },
        }))
        return
      }
      setState((prev) => ({ ...prev, projectIds: [], status: idle }))
      onOpenChange(false)
    } catch (err) {
      setState((prev) => ({ ...prev, status: submitError(err, 'Could not grant access') }))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85svh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Grant to a project</DialogTitle>
          <DialogDescription>Make {resourceName} available to a project.</DialogDescription>
        </DialogHeader>
        <form onSubmit={(event) => void submit(event)}>
          <FieldGroup>
            <ProjectGrantsField
              orgId={orgId}
              isProjectEligible={isProjectEligible}
              value={state.projectIds}
              onChange={(projectIds) => {
                setState((prev) => ({ ...prev, projectIds }))
              }}
              disabled={isSubmitting}
              excludedProjectIds={excludedProjectIds}
              description="Add one or more projects to grant access."
            />
            {options}
            {errorMessage && <p className="text-destructive text-sm">{errorMessage}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={isSubmitting || submitDisabled || state.projectIds.length === 0}
              >
                {isSubmitting && <Spinner />}
                Grant
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
