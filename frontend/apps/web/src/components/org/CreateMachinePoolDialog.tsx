import { useCreateMachinePool, useGrantMachinePoolToProject } from '@omnara/react'
import type { MachinePool } from '@omnara/sdk'
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
import { collectGrantFailures, type RetryGrantsPhase } from '@/lib/grant-failures'
import { errorMessage } from '@/lib/submit-status'

import { MachinePoolAdvancedSection } from './MachinePoolAdvancedSection'
import {
  machinePoolCreateRequest,
  machinePoolFormDefaults,
  machinePoolFormValid,
  type MachinePoolFormValues,
} from './MachinePoolDialogState'
import { MachinePoolFields, type MachinePoolFormSetValue } from './MachinePoolFields'

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
  const [values, setValues] = useState<MachinePoolFormValues>(machinePoolFormDefaults)
  const [submitting, setSubmitting] = useState(false)
  const valid = machinePoolFormValid(values)

  const setValue: MachinePoolFormSetValue = (key, value) => {
    setValues((previous) => ({ ...previous, [key]: value }))
  }

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (submitting) return
    setSubmitting(true)
    setPhase((prev) => ({ ...prev, error: '' }))
    try {
      let pool = phase.kind === 'retry-grants' ? phase.created : null
      pool ??= await createMachinePool.mutateAsync(machinePoolCreateRequest(values))
      const grantResults = await Promise.allSettled(
        values.projectGrantIds.map((projectID) =>
          grantMachinePool.mutateAsync({ projectID, machine_pool_id: pool.id }),
        ),
      )
      const failures = collectGrantFailures(values.projectGrantIds, grantResults)
      if (failures) {
        setValue('projectGrantIds', failures.failedProjectIds)
        setPhase({
          kind: 'retry-grants',
          created: pool,
          error: `The pool was created, but ${failures.message}`,
        })
        return
      }
      // Keep the machine sizing so consecutive pools reuse it.
      setValues({
        ...machinePoolFormDefaults,
        cpu: values.cpu,
        memoryGb: values.memoryGb,
        maxMachines: values.maxMachines,
      })
      setPhase({ kind: 'form', error: '' })
      onOpenChange(false)
    } catch (err) {
      setPhase((prev) => ({ ...prev, error: errorMessage(err, 'Could not create pool') }))
    } finally {
      setSubmitting(false)
    }
  }

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
        <form onSubmit={(event) => void submit(event)}>
          <FieldGroup>
            <MachinePoolFields
              orgId={orgId}
              enabled={open}
              mode="create"
              values={values}
              setValue={setValue}
            />
            <ProjectGrantsField
              orgId={orgId}
              isProjectEligible={(project) => project.access.can_manage_access}
              value={values.projectGrantIds}
              onChange={(projectGrantIds) => {
                setValue('projectGrantIds', projectGrantIds)
              }}
              disabled={submitting}
            />
            <MachinePoolAdvancedSection
              orgId={orgId}
              enabled={open}
              clusterManaged={false}
              values={values}
              setValue={setValue}
            />
            {phase.error && <p className="text-destructive text-sm">{phase.error}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={submitting || (phase.kind === 'form' && !valid)}
                loading={submitting}
              >
                {phase.kind === 'retry-grants' ? 'Retry project grants' : 'Create pool'}
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
