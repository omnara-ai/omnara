import { useCreateSecretVersion, useUpdateSecret } from '@omnara/react'
import { ApiError, type Secret } from '@omnara/sdk'
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
  const [savedName, setSavedName] = useState(secret.name)
  const [name, setName] = useState(secret.name)
  const [updates, setUpdates] = useState<Record<string, string>>({})
  const [lifetime, setLifetime] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const canEditValue =
    secret.management_kind === 'tenant' && requiredSecretKeys(secret.kind) !== undefined
  const changingToken = secret.kind === 'oauth_token_set' && updates.access_token !== undefined
  const replacement = prepareSecretReplacement(secret, updates, lifetime)
  const nameChanged = normalizeResourceName(name) !== savedName
  const hasUpdates = Object.keys(updates).length > 0
  const valid = resourceNameValid(name) && !replacement.error && (nameChanged || hasUpdates)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (submitting || !valid) return
    setSubmitting(true)
    setError('')
    let renamed = false
    try {
      if (nameChanged) {
        const updated = await updateSecret.mutateAsync({
          secretID: secret.id,
          name: normalizeResourceName(name),
        })
        setSavedName(updated.name)
        renamed = true
      }
      if (replacement.material) await createVersion.mutateAsync({ material: replacement.material })
      onOpenChange(false)
    } catch (error) {
      const message = error instanceof ApiError ? error.message : 'Could not update secret'
      setError(renamed ? `Name saved, but the value update failed. ${message}` : message)
    } finally {
      setSubmitting(false)
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
                  value={name}
                  onChange={(event) => {
                    setName(event.target.value)
                  }}
                />
                <ResourceNameFieldError value={name} />
              </Field>
              {canEditValue && (
                <SecretValueEditor
                  secret={secret}
                  updates={updates}
                  onChange={(next) => {
                    if (next.access_token === undefined) setLifetime('')
                    setUpdates(next)
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
                      setLifetime(event.target.value)
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
