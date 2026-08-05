import { useConnectMachine } from '@omnara/react'
import { CheckIcon, CopyIcon } from 'lucide-react'
import { type SyntheticEvent, useReducer } from 'react'

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
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/lib/submit-status'

/**
 * The dialog is a two-step flow: fill in the form, then show the install command
 * and machine token. `connected` is the only transition between steps, and
 * closing the dialog resets to a fresh form.
 */
type ConnectMachineState =
  | { step: 'form'; name: string; projectGrantIds: string[]; submitting: boolean; error: string }
  | {
      step: 'result'
      machineToken: string
      grantWarning: string
      copied: 'command' | 'token' | null
    }

type ConnectMachineAction =
  | { type: 'setName'; name: string }
  | { type: 'setProjectGrantIds'; projectGrantIds: string[] }
  | { type: 'submitStart' }
  | { type: 'submitFail'; message: string }
  | { type: 'connected'; machineToken: string; grantWarning: string }
  | { type: 'copied'; target: 'command' | 'token' }
  | { type: 'reset' }

const initialState: ConnectMachineState = {
  step: 'form',
  name: '',
  projectGrantIds: [],
  submitting: false,
  error: '',
}

function reducer(state: ConnectMachineState, action: ConnectMachineAction): ConnectMachineState {
  switch (action.type) {
    case 'setName':
      return state.step === 'form' ? { ...state, name: action.name } : state
    case 'setProjectGrantIds':
      return state.step === 'form' ? { ...state, projectGrantIds: action.projectGrantIds } : state
    case 'submitStart':
      return state.step === 'form' ? { ...state, submitting: true, error: '' } : state
    case 'submitFail':
      return state.step === 'form' ? { ...state, submitting: false, error: action.message } : state
    case 'connected':
      return {
        step: 'result',
        machineToken: action.machineToken,
        grantWarning: action.grantWarning,
        copied: null,
      }
    case 'copied':
      return state.step === 'result' ? { ...state, copied: action.target } : state
    case 'reset':
      return initialState
  }
}

export function ConnectMachineDialog({
  open,
  onOpenChange,
  orgId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
}) {
  const connectMachine = useConnectMachine(orgId)
  const [state, dispatch] = useReducer(reducer, initialState)
  const installCommand = `curl -fsSL ${window.location.origin}/install/omnarad.sh | sh`

  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) {
      dispatch({ type: 'reset' })
    }
    onOpenChange(nextOpen)
  }

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (state.step !== 'form') {
      return
    }
    dispatch({ type: 'submitStart' })
    try {
      const { token, failedProjectGrants } = await connectMachine.mutateAsync({
        displayName: state.name.trim(),
        projectIDs: state.projectGrantIds,
      })
      const detail = failedProjectGrants[0]?.message
      dispatch({
        type: 'connected',
        machineToken: token.token,
        grantWarning:
          failedProjectGrants.length > 0
            ? `${String(failedProjectGrants.length)} project grant${failedProjectGrants.length === 1 ? '' : 's'} could not be added${detail ? `: ${detail}` : ''}. Retry from the machine's actions menu.`
            : '',
      })
    } catch (err) {
      dispatch({ type: 'submitFail', message: errorMessage(err, 'Could not connect machine') })
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Connect a machine</DialogTitle>
          <DialogDescription>
            Register a machine you operate and install its daemon.
          </DialogDescription>
        </DialogHeader>
        {state.step === 'form' ? (
          <form
            onSubmit={(event) => {
              void submit(event)
            }}
          >
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="machine-name">Machine name</FieldLabel>
                <Input
                  id="machine-name"
                  required
                  value={state.name}
                  placeholder="my-macbook"
                  onChange={(event) => {
                    dispatch({ type: 'setName', name: event.target.value })
                  }}
                />
                <FieldDescription>
                  Agent configs reference this name via machine_sources.machine_name.
                </FieldDescription>
              </Field>
              <ProjectGrantsField
                orgId={orgId}
                isProjectEligible={(project) => project.access.can_manage_access}
                value={state.projectGrantIds}
                onChange={(projectGrantIds) => {
                  dispatch({ type: 'setProjectGrantIds', projectGrantIds })
                }}
                disabled={state.submitting}
                description="Agents in selected projects will be able to run commands on the machine."
              />
              {state.error && (
                <p className="text-destructive whitespace-pre-wrap text-sm">{state.error}</p>
              )}
              <DialogFooter>
                <Button type="submit" disabled={state.submitting || state.name.trim() === ''}>
                  {state.submitting && <Spinner />}
                  Connect machine
                </Button>
              </DialogFooter>
            </FieldGroup>
          </form>
        ) : (
          <FieldGroup>
            <Field>
              <FieldLabel>Install command</FieldLabel>
              <pre className="bg-muted max-h-56 min-w-0 overflow-y-auto whitespace-pre-wrap break-all rounded-md p-3 font-mono text-xs">
                {installCommand}
              </pre>
              <FieldDescription>
                Run this on the machine, then paste the machine token when prompted.
              </FieldDescription>
              <Button
                type="button"
                variant="outline"
                onClick={() => {
                  void navigator.clipboard.writeText(installCommand).then(() => {
                    dispatch({ type: 'copied', target: 'command' })
                  })
                }}
              >
                {state.copied === 'command' ? <CheckIcon /> : <CopyIcon />}
                {state.copied === 'command' ? 'Copied' : 'Copy command'}
              </Button>
            </Field>
            <Field>
              <FieldLabel>Machine token</FieldLabel>
              <pre className="bg-muted min-w-0 overflow-y-auto whitespace-pre-wrap break-all rounded-md p-3 font-mono text-xs">
                {state.machineToken}
              </pre>
              <FieldDescription>Shown once.</FieldDescription>
              <Button
                type="button"
                variant="outline"
                onClick={() => {
                  void navigator.clipboard.writeText(state.machineToken).then(() => {
                    dispatch({ type: 'copied', target: 'token' })
                  })
                }}
              >
                {state.copied === 'token' ? <CheckIcon /> : <CopyIcon />}
                {state.copied === 'token' ? 'Copied' : 'Copy token'}
              </Button>
            </Field>
            {state.grantWarning && <p className="text-warning text-sm">{state.grantWarning}</p>}
            <DialogFooter>
              <Button
                type="button"
                onClick={() => {
                  handleOpenChange(false)
                }}
              >
                Done
              </Button>
            </DialogFooter>
          </FieldGroup>
        )}
      </DialogContent>
    </Dialog>
  )
}
