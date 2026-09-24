import { useCreateSecret } from '@omnara/react'
import type { Secret, SecretOwnerInput } from '@omnara/sdk'
import { useId, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldGroup, RequiredFieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import { resourceNameValid } from '@/lib/resource-name'
import { errorMessage } from '@/lib/submit-status'

export function NewSecretDialog({
  orgId,
  projectId,
  defaultName = '',
  onClose,
  onCreated,
}: {
  orgId: string
  projectId?: string
  defaultName?: string
  onClose: () => void
  onCreated: (secret: Secret) => void
}) {
  const idPrefix = useId()
  const createSecret = useCreateSecret(orgId)
  const [name, setName] = useState(defaultName)
  const [value, setValue] = useState('')
  const [error, setError] = useState('')
  const valid = resourceNameValid(name) && value !== ''
  const owner: SecretOwnerInput =
    projectId === undefined ? { kind: 'org' } : { kind: 'project', project_id: projectId }

  async function submit() {
    setError('')
    try {
      const secret = await createSecret.mutateAsync({
        owner,
        name: name.trim(),
        material: { kind: 'generic', value },
      })
      onCreated(secret)
    } catch (err) {
      setError(errorMessage(err, 'Could not create secret'))
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !createSecret.isPending) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New secret</DialogTitle>
          <DialogDescription>
            {projectId === undefined
              ? 'Stored as an organization secret.'
              : 'Stored as a project secret.'}
          </DialogDescription>
        </DialogHeader>
        <form
          autoComplete="off"
          onSubmit={(event) => {
            event.preventDefault()
            if (valid && !createSecret.isPending) void submit()
          }}
        >
          <FieldGroup>
            <Field>
              <RequiredFieldLabel htmlFor={`${idPrefix}-name`}>Secret name</RequiredFieldLabel>
              <Input
                id={`${idPrefix}-name`}
                value={name}
                autoComplete="off"
                placeholder="api-key"
                onChange={(event) => {
                  setName(event.target.value)
                }}
              />
              <ResourceNameFieldError value={name} />
            </Field>
            <Field>
              <RequiredFieldLabel htmlFor={`${idPrefix}-value`}>Value</RequiredFieldLabel>
              <Input
                id={`${idPrefix}-value`}
                type="password"
                value={value}
                autoComplete="new-password"
                onChange={(event) => {
                  setValue(event.target.value)
                }}
              />
            </Field>
            {error && <p className="text-destructive text-sm">{error}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={createSecret.isPending || !valid}
                loading={createSecret.isPending}
              >
                Create secret
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
