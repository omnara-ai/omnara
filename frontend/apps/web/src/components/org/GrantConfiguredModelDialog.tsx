import { useCreateProjectModelGrant } from '@omnara/react'
import { type ConfiguredModel } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

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
import { collectGrantFailures } from '@/lib/grant-failures'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, submitting } from '@/lib/submit-status'

interface GrantConfiguredModelState {
  projectIds: string[]
  // Grants run as parallel mutateAsync calls, so the mutation's isPending is
  // not a reliable in-flight signal; track the submitting phase explicitly.
  status: SubmitStatus
}

export function GrantConfiguredModelDialog({
  open,
  onOpenChange,
  orgId,
  model,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  model: ConfiguredModel
}) {
  const createProjectModelGrant = useCreateProjectModelGrant(orgId)
  const [state, setState] = useState<GrantConfiguredModelState>({
    projectIds: [],
    status: idle,
  })
  const isSubmitting = state.status.phase === 'submitting'
  const errorMessage = statusError(state.status)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setState((prev) => ({ ...prev, status: submitting }))
    try {
      const results = await Promise.allSettled(
        state.projectIds.map((projectID) =>
          createProjectModelGrant.mutateAsync({
            projectID,
            configured_model_id: model.id,
          }),
        ),
      )
      const failures = collectGrantFailures(state.projectIds, results)
      if (failures) {
        setState({
          projectIds: failures.failedProjectIds,
          status: { phase: 'error', message: failures.message },
        })
        return
      }
      setState({ projectIds: [], status: idle })
      onOpenChange(false)
    } catch (err) {
      setState((prev) => ({ ...prev, status: submitError(err, 'Could not grant model') }))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Grant configured model</DialogTitle>
          <DialogDescription>Let agents in a project use {model.name}.</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            void submit(event)
          }}
        >
          <FieldGroup>
            <ProjectGrantsField
              orgId={orgId}
              isProjectEligible={(project) => project.access.can_manage_access}
              value={state.projectIds}
              onChange={(projectIds) => {
                setState((prev) => ({ ...prev, projectIds }))
              }}
              disabled={isSubmitting}
              description={`Selected projects will be able to use ${model.name}.`}
            />
            {errorMessage && <p className="text-destructive text-sm">{errorMessage}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={isSubmitting || state.projectIds.length === 0}
                loading={isSubmitting}
              >
                Grant model
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
