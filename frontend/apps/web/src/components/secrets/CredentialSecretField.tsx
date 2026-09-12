import { useCreateSecret } from '@omnara/react'
import type { Secret } from '@omnara/sdk'
import { type KeyboardEvent, useState } from 'react'

import { AWSCredentialsSecretFields } from '@/components/org/AWSCredentialsSecretFields'
import {
  awsCredentialsMaterial,
  newAWSCredentialsSecret,
} from '@/components/org/CreateSecretDialogState'
import { SecretTypeaheadField } from '@/components/secrets/SecretTypeaheadField'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import { resourceNameValid } from '@/lib/resource-name'
import { errorMessage } from '@/lib/submit-status'

export function CredentialSecretField({
  orgId,
  enabled,
  value,
  onChange,
  label,
  placeholder,
  emptyDescription,
  defaultSecretName = '',
  secretValuePlaceholder = 'sk-…',
  kind = 'generic',
}: {
  orgId: string
  enabled: boolean
  value: string
  onChange: (value: string) => void
  label: string
  placeholder?: string
  emptyDescription: string
  defaultSecretName?: string
  secretValuePlaceholder?: string
  kind?: 'generic' | 'aws_credentials'
}) {
  const [creating, setCreating] = useState(false)
  const [createdSecret, setCreatedSecret] = useState<Secret>()

  if (!creating) {
    return (
      <SecretTypeaheadField
        orgId={orgId}
        enabled={enabled}
        value={value}
        onChange={onChange}
        label={label}
        placeholder={placeholder}
        emptyDescription={emptyDescription}
        knownSecret={createdSecret}
        kind={kind}
        onCreateSecret={() => {
          setCreating(true)
        }}
      />
    )
  }
  return (
    <InlineNewSecretFields
      orgId={orgId}
      label={label}
      defaultName={defaultSecretName}
      valuePlaceholder={secretValuePlaceholder}
      kind={kind}
      onCancel={() => {
        setCreating(false)
      }}
      onCreated={(secret) => {
        setCreatedSecret(secret)
        onChange(secret.id)
        setCreating(false)
      }}
    />
  )
}

function InlineNewSecretFields({
  orgId,
  label,
  defaultName,
  valuePlaceholder,
  kind,
  onCancel,
  onCreated,
}: {
  orgId: string
  label: string
  defaultName: string
  valuePlaceholder: string
  kind: 'generic' | 'aws_credentials'
  onCancel: () => void
  onCreated: (secret: Secret) => void
}) {
  const createSecret = useCreateSecret(orgId)
  const [name, setName] = useState(defaultName)
  const [secretValue, setSecretValue] = useState('')
  const [awsCredentials, setAWSCredentials] = useState(newAWSCredentialsSecret)
  const [error, setError] = useState('')
  const awsMaterial = awsCredentialsMaterial(awsCredentials)
  const valid =
    resourceNameValid(name) &&
    (kind === 'aws_credentials' ? awsMaterial !== undefined : secretValue !== '')

  async function submit() {
    setError('')
    try {
      const material =
        kind === 'aws_credentials' ? awsMaterial : { kind: 'generic' as const, value: secretValue }
      if (material === undefined) return
      const secret = await createSecret.mutateAsync({
        owner: { kind: 'org' },
        name,
        material,
      })
      onCreated(secret)
    } catch (err) {
      setError(errorMessage(err, 'Could not create secret'))
    }
  }

  function submitOnEnter(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key !== 'Enter') return
    event.preventDefault()
    if (valid && !createSecret.isPending) {
      void submit()
    }
  }

  return (
    <Field>
      <FieldLabel>{label}</FieldLabel>
      <div className="grid gap-3 rounded-md border p-3">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field>
            <FieldLabel htmlFor="credential-secret-name">Secret name</FieldLabel>
            <Input
              id="credential-secret-name"
              value={name}
              autoComplete="off"
              placeholder="api-key"
              onChange={(event) => {
                setName(event.target.value)
              }}
              onKeyDown={submitOnEnter}
            />
            <ResourceNameFieldError value={name} />
          </Field>
          {kind === 'aws_credentials' ? (
            <AWSCredentialsSecretFields
              value={awsCredentials}
              onChange={(patch) => {
                setAWSCredentials((current) => ({ ...current, ...patch }))
              }}
            />
          ) : (
            <Field>
              <FieldLabel htmlFor="credential-secret-value">API key</FieldLabel>
              <Input
                id="credential-secret-value"
                type="password"
                value={secretValue}
                autoComplete="new-password"
                placeholder={valuePlaceholder}
                onChange={(event) => {
                  setSecretValue(event.target.value)
                }}
                onKeyDown={submitOnEnter}
              />
            </Field>
          )}
        </div>
        {error && <p className="text-destructive text-sm">{error}</p>}
        <div className="flex items-center justify-between gap-2">
          <FieldDescription>Stored as an organization secret.</FieldDescription>
          <div className="flex gap-2">
            <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
              Cancel
            </Button>
            <Button
              type="button"
              size="sm"
              disabled={createSecret.isPending || !valid}
              loading={createSecret.isPending}
              onClick={() => {
                void submit()
              }}
            >
              Create secret
            </Button>
          </div>
        </div>
      </div>
    </Field>
  )
}
