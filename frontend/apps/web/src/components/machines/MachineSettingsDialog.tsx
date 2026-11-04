import { useMachine, useUpdateMachine } from '@omnara/react'
import type { Machine } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import { programmaticToolCallsWarning } from '@/components/machines/programmaticToolCalls'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { CheckboxField, FieldGroup } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/lib/submit-status'

/** Settings for one machine; today, whether its commands can call their agent's tools. */
export function MachineSettingsDialog({
  open,
  onOpenChange,
  orgId,
  machineId,
  machineName,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  machineId: string
  machineName: string
}) {
  const machine = useMachine(orgId, machineId)
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Machine settings</DialogTitle>
          <DialogDescription>Change settings for {machineName}.</DialogDescription>
        </DialogHeader>
        {machine.data ? (
          <MachineSettingsForm
            orgId={orgId}
            machine={machine.data}
            onSaved={() => {
              onOpenChange(false)
            }}
          />
        ) : machine.isError ? (
          <p className="text-destructive text-sm">
            {errorMessage(machine.error, 'Could not load machine')}
          </p>
        ) : (
          <Spinner />
        )}
      </DialogContent>
    </Dialog>
  )
}

function MachineSettingsForm({
  orgId,
  machine,
  onSaved,
}: {
  orgId: string
  machine: Machine
  onSaved: () => void
}) {
  const updateMachine = useUpdateMachine(orgId)
  // Unchanged until toggled, so the form shows the machine's saved value.
  const [toggled, setProgrammaticToolCalls] = useState<boolean>()
  const programmaticToolCalls = toggled ?? machine.programmatic_tool_calls

  function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    updateMachine.mutate(
      {
        machineID: machine.id,
        body: { programmatic_tool_calls: programmaticToolCalls },
      },
      { onSuccess: onSaved },
    )
  }

  return (
    <form onSubmit={submit}>
      <FieldGroup>
        <CheckboxField
          label="Programmatic tool calls"
          description={
            <>
              Let commands call their agent’s tools with <code>omnarad call</code>, for agents whose
              config enables it. {programmaticToolCallsWarning}
            </>
          }
          inputClassName="self-start"
          checked={programmaticToolCalls}
          onChange={(event) => {
            setProgrammaticToolCalls(event.target.checked)
          }}
        />
        {updateMachine.isError && (
          <p className="text-destructive text-sm">
            {errorMessage(updateMachine.error, 'Could not save machine settings')}
          </p>
        )}
        <DialogFooter>
          <Button
            type="submit"
            disabled={
              updateMachine.isPending || programmaticToolCalls === machine.programmatic_tool_calls
            }
            loading={updateMachine.isPending}
          >
            Save
          </Button>
        </DialogFooter>
      </FieldGroup>
    </form>
  )
}
