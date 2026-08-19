import { useCreateMachinePool, useGrantMachinePoolToProject } from '@omnara/react'
import type { MachinePool } from '@omnara/sdk'
import { useForm } from '@tanstack/react-form'
import { useState } from 'react'

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
import { collectGrantFailures, type RetryGrantsPhase } from '@/lib/grant-failures'
import { errorMessage } from '@/lib/submit-status'

import {
  machinePoolCreateRequest,
  machinePoolFormDefaults,
  machinePoolFormValid,
} from './MachinePoolDialogState'
import { MachinePoolFields } from './MachinePoolFields'

export function CreateMachinePoolDialog({
  open,
  onOpenChange,
  orgId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
}) {
  const createMachinePool = useCreateMachinePool(orgId)
  const grantMachinePool = useGrantMachinePoolToProject(orgId)
  const [phase, setPhase] = useState<RetryGrantsPhase<MachinePool>>({ kind: 'form', error: '' })
  const form = useForm({
    defaultValues: machinePoolFormDefaults,
    onSubmit: async ({ value }) => {
      setPhase((prev) => ({ ...prev, error: '' }))
      try {
        let pool = phase.kind === 'retry-grants' ? phase.created : null
        pool ??= await createMachinePool.mutateAsync(machinePoolCreateRequest(value))
        const grantResults = await Promise.allSettled(
          value.projectGrantIds.map((projectID) =>
            grantMachinePool.mutateAsync({ projectID, machine_pool_id: pool.id }),
          ),
        )
        const failures = collectGrantFailures(value.projectGrantIds, grantResults)
        if (failures) {
          form.setFieldValue('projectGrantIds', failures.failedProjectIds)
          setPhase({
            kind: 'retry-grants',
            created: pool,
            error: `The pool was created, but ${failures.message}`,
          })
          return
        }
        // Keep the machine sizing so consecutive pools reuse it.
        form.reset({
          ...machinePoolFormDefaults,
          cpu: value.cpu,
          memoryGb: value.memoryGb,
          maxMachines: value.maxMachines,
        })
        setPhase({ kind: 'form', error: '' })
        onOpenChange(false)
      } catch (err) {
        setPhase((prev) => ({ ...prev, error: errorMessage(err, 'Could not create pool') }))
      }
    },
  })
  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) setPhase({ kind: 'form', error: '' })
    onOpenChange(nextOpen)
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-h-[85svh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>New machine pool</DialogTitle>
          <DialogDescription>Pools provision the machines your agents run on.</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void form.handleSubmit()
          }}
        >
          <FieldGroup>
            <form.Subscribe selector={(state) => state.values}>
              {(values) => (
                <MachinePoolFields
                  orgId={orgId}
                  enabled={open}
                  mode="create"
                  values={values}
                  setValue={(key, value) => {
                    form.setFieldValue(key, value as never)
                  }}
                />
              )}
            </form.Subscribe>
            <form.Field name="projectGrantIds">
              {(field) => (
                <form.Subscribe selector={(state) => state.isSubmitting}>
                  {(isSubmitting) => (
                    <ProjectGrantsField
                      orgId={orgId}
                      isProjectEligible={(project) => project.access.can_manage_access}
                      value={field.state.value}
                      onChange={field.handleChange}
                      disabled={isSubmitting}
                    />
                  )}
                </form.Subscribe>
              )}
            </form.Field>
            {phase.error && <p className="text-destructive text-sm">{phase.error}</p>}
            <DialogFooter>
              <form.Subscribe
                selector={(state) =>
                  [machinePoolFormValid(state.values), state.isSubmitting] as const
                }
              >
                {([valid, isSubmitting]) => (
                  <Button
                    type="submit"
                    disabled={isSubmitting || (phase.kind === 'form' && !valid)}
                    loading={isSubmitting}
                  >
                    {phase.kind === 'retry-grants' ? 'Retry project grants' : 'Create pool'}
                  </Button>
                )}
              </form.Subscribe>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
