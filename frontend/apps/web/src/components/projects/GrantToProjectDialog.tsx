import type { VisibleProject } from '@omnara/sdk'
import { type ReactNode, type SyntheticEvent, useState } from 'react'

import { ProjectShareChips } from '@/components/projects/ProjectShareChips'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup } from '@/components/ui/field'
import { collectGrantFailures } from '@/lib/grant-failures'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, submitting } from '@/lib/submit-status'

interface GrantToProjectState {
  projectIds: string[]
  status: SubmitStatus
}

const noExcludedProjectIds: string[] = []

export function GrantToProjectDialog<TGrant>({
  open,
  onOpenChange,
  orgId,
  resourceName,
  onGrant,
  isProjectEligible,
  excludedProjectIds = noExcludedProjectIds,
  options,
  submitDisabled = false,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  resourceName: string
  onGrant: (projectId: string) => Promise<TGrant>
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
      const status = submitError(err, 'Could not share')
      setState((prev) => ({ ...prev, status }))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85svh] sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Share with projects</DialogTitle>
          <DialogDescription>Make {resourceName} available to a project.</DialogDescription>
        </DialogHeader>
        <form onSubmit={(event) => void submit(event)}>
          <FieldGroup>
            <Field>
              <ProjectShareChips
                orgId={orgId}
                isProjectEligible={isProjectEligible}
                value={state.projectIds}
                onChange={(projectIds) => {
                  setState((prev) => ({ ...prev, projectIds }))
                }}
                disabled={isSubmitting}
                excludedProjectIds={excludedProjectIds}
              />
              <FieldDescription>Add one or more projects to share with.</FieldDescription>
            </Field>
            {options}
            {errorMessage && <p className="text-destructive text-sm">{errorMessage}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={isSubmitting || submitDisabled || state.projectIds.length === 0}
                loading={isSubmitting}
              >
                Share
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
