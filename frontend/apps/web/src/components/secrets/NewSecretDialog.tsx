import { useCreateSecret } from '@omnara/react'
import type { Secret, SecretOwnerInput } from '@omnara/sdk'
import { useId, useState } from 'react'

import { CopyButton } from '@/components/overview/CodeBlock'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  RequiredFieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import { resourceNameValid } from '@/lib/resource-name'
import { errorMessage } from '@/lib/submit-status'

export function NewSecretDialog({
  orgId,
  projectId,
  defaultName = '',
  valueError,
  generateValue,
  onClose,
  onCreated,
}: {
  orgId: string
  projectId?: string
  defaultName?: string
  valueError?: (value: string) => string | undefined
  generateValue?: () => string
  onClose: () => void
  onCreated: (secret: Secret) => void
}) {
  const idPrefix = useId()
  const createSecret = useCreateSecret(orgId)
  const [name, setName] = useState(defaultName)
  const [value, setValue] = useState('')
  const [error, setError] = useState('')
  const [generated, setGenerated] = useState(false)
  const valueMessage = value === '' ? undefined : valueError?.(value)
  const valid = resourceNameValid(name) && value !== '' && valueMessage === undefined
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
              <div className="flex items-center justify-between">
                <RequiredFieldLabel htmlFor={`${idPrefix}-value`}>Value</RequiredFieldLabel>
                {generateValue && (
                  <Button
                    type="button"
                    variant="link"
                    size="sm"
                    className="type-control-small h-auto p-0"
                    disabled={createSecret.isPending}
                    onClick={() => {
                      setValue(generateValue())
                      setGenerated(true)
                    }}
                  >
                    Generate
                  </Button>
                )}
              </div>
              <div className="flex items-center gap-2">
                <Input
                  id={`${idPrefix}-value`}
                  type={generated ? 'text' : 'password'}
                  className={generated ? 'font-mono' : undefined}
                  value={value}
                  disabled={createSecret.isPending}
                  autoComplete="new-password"
                  onChange={(event) => {
                    setValue(event.target.value)
                  }}
                />
                {generated && <CopyButton key={value} text={value} label="secret" />}
              </div>
              {valueMessage && <FieldError>{valueMessage}</FieldError>}
              {generated && (
                <FieldDescription>Shown once. Copy it before creating the secret.</FieldDescription>
              )}
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
