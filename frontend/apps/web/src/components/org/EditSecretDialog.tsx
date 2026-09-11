import { useCreateSecretVersion, useUpdateSecret } from '@omnara/react'
import { ApiError, type Secret } from '@omnara/sdk'
import { type SyntheticEvent, useReducer } from 'react'

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
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import { normalizeResourceName, resourceNameValid } from '@/lib/resource-name'

import { SecretValueEditor } from './SecretValueEditor'
import { prepareSecretReplacement, requiredSecretKeys } from './secretValueUpdates'

export function EditSecretDialog({
  open,
  onOpenChange,
  orgId,
  secret,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  secret: Secret
}) {
  const updateSecret = useUpdateSecret(orgId)
  const createVersion = useCreateSecretVersion(orgId, secret.id)
  const [state, dispatch] = useReducer(editSecretReducer, {
    draftName: secret.name,
    persistedName: secret.name,
    updates: {},
    lifetime: '',
    error: '',
    submitting: false,
  })
  const { draftName, persistedName, updates, lifetime, error, submitting } = state
  const canEditValue =
    secret.management_kind === 'tenant' && requiredSecretKeys(secret.kind) !== undefined
  const changingToken = secret.kind === 'oauth_token_set' && updates.access_token !== undefined
  const replacement = prepareSecretReplacement(secret, updates, lifetime)
  const nameChanged = normalizeResourceName(draftName) !== persistedName
  const hasUpdates = Object.keys(updates).length > 0
  const valid = resourceNameValid(draftName) && !replacement.error && (nameChanged || hasUpdates)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (submitting || !valid) return
    dispatch({ type: 'submit-started' })
    let renamed = false
    try {
      if (nameChanged) {
        const updated = await updateSecret.mutateAsync({
          secretID: secret.id,
          name: normalizeResourceName(draftName),
        })
        dispatch({ type: 'name-saved', name: updated.name })
        renamed = true
      }
      if (replacement.material) await createVersion.mutateAsync({ material: replacement.material })
      dispatch({ type: 'submit-finished' })
      onOpenChange(false)
    } catch (error) {
      const message = error instanceof ApiError ? error.message : 'Could not update secret'
      dispatch({
        type: 'submit-failed',
        message: renamed ? `Name saved, but the value update failed. ${message}` : message,
      })
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (!submitting) onOpenChange(nextOpen)
      }}
    >
      <DialogContent showCloseButton={!submitting}>
        <DialogHeader>
          <DialogTitle>Edit secret</DialogTitle>
          <DialogDescription>
            {!canEditValue
              ? 'Only the name can be edited here.'
              : secret.kind === 'generic'
                ? 'Stored values stay hidden. Enter a new value to replace it.'
                : 'Stored values stay hidden. To update credentials, enter the complete replacement set.'}
          </DialogDescription>
        </DialogHeader>
        <form autoComplete="off" onSubmit={(event) => void submit(event)}>
          <fieldset disabled={submitting}>
            <FieldGroup className="gap-4">
              <Field>
                <FieldLabel htmlFor="edit-secret-name">Name</FieldLabel>
                <Input
                  id="edit-secret-name"
                  required
                  value={draftName}
                  onChange={(event) => {
                    dispatch({ type: 'name-changed', name: event.target.value })
                  }}
                />
                <ResourceNameFieldError value={draftName} />
              </Field>
              {canEditValue && (
                <SecretValueEditor
                  secret={secret}
                  updates={updates}
                  onChange={(next) => {
                    dispatch({ type: 'credentials-changed', updates: next })
                  }}
                />
              )}
              {changingToken && (
                <Field>
                  <FieldLabel htmlFor="token-lifetime">Access token lifetime (seconds)</FieldLabel>
                  <Input
                    id="token-lifetime"
                    type="number"
                    min={1}
                    max={2147483647}
                    value={lifetime}
                    onChange={(event) => {
                      dispatch({ type: 'lifetime-changed', lifetime: event.target.value })
                    }}
                    placeholder="No expiry"
                  />
                  <FieldDescription>
                    Applies to the replacement token. Leave blank for no expiry.
                  </FieldDescription>
                </Field>
              )}
              {replacement.error && (
                <p role="alert" className="text-destructive text-sm">
                  {replacement.error}
                </p>
              )}
              {error && (
                <p role="alert" className="text-destructive text-sm">
                  {error}
                </p>
              )}
              <DialogFooter>
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    onOpenChange(false)
                  }}
                >
                  Cancel
                </Button>
                <Button type="submit" disabled={submitting || !valid} loading={submitting}>
                  Save changes
                </Button>
              </DialogFooter>
            </FieldGroup>
          </fieldset>
        </form>
      </DialogContent>
    </Dialog>
  )
}

interface EditSecretState {
  draftName: string
  persistedName: string
  updates: Record<string, string>
  lifetime: string
  error: string
  submitting: boolean
}

type EditSecretAction =
  | { type: 'name-changed'; name: string }
  | { type: 'name-saved'; name: string }
  | { type: 'credentials-changed'; updates: Record<string, string> }
  | { type: 'lifetime-changed'; lifetime: string }
  | { type: 'submit-started' }
  | { type: 'submit-failed'; message: string }
  | { type: 'submit-finished' }

function editSecretReducer(state: EditSecretState, action: EditSecretAction): EditSecretState {
  switch (action.type) {
    case 'name-changed':
      return { ...state, draftName: action.name }
    case 'name-saved':
      return { ...state, persistedName: action.name }
    case 'credentials-changed':
      return {
        ...state,
        updates: action.updates,
        lifetime: action.updates.access_token === undefined ? '' : state.lifetime,
      }
    case 'lifetime-changed':
      return { ...state, lifetime: action.lifetime }
    case 'submit-started':
      return { ...state, submitting: true, error: '' }
    case 'submit-failed':
      return { ...state, submitting: false, error: action.message }
    case 'submit-finished':
      return { ...state, submitting: false }
  }
}
