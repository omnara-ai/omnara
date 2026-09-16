import { useCreateSecret } from '@omnara/react'
import type { IntegrationAppProvider, Secret, SecretOwnerInput } from '@omnara/sdk'
import { type KeyboardEvent, useState } from 'react'

import { AWSCredentialsSecretFields } from '@/components/org/AWSCredentialsSecretFields'
import {
  awsCredentialsMaterial,
  newAWSCredentialsSecret,
} from '@/components/org/CreateSecretDialogState'
import {
  integrationCredentialsMaterial,
  integrationFields,
  newIntegrationCredentials,
} from '@/components/org/integrationCredentials'
import { IntegrationCredentialsFields } from '@/components/org/IntegrationCredentialsFields'
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
  owner = { kind: 'org' },
  integrationProvider = 'github',
  onPendingChange,
  onCreatingChange,
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
  kind?: 'generic' | 'aws_credentials' | 'integration_credentials'
  owner?: SecretOwnerInput
  integrationProvider?: IntegrationAppProvider
  onPendingChange?: (pending: boolean) => void
  onCreatingChange?: (creating: boolean) => void
}) {
  const [creating, setCreating] = useState(false)
  const [createdSecret, setCreatedSecret] = useState<Secret>()
  const [previousSecretId, setPreviousSecretId] = useState('')

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
        owner={owner}
        requiredPayloadKeys={
          kind === 'integration_credentials'
            ? integrationFields(integrationProvider).map((field) => field.value)
            : undefined
        }
        onCreateSecret={() => {
          onCreatingChange?.(true)
          setPreviousSecretId(value)
          onChange('')
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
      owner={owner}
      integrationProvider={integrationProvider}
      onPendingChange={onPendingChange}
      onCancel={() => {
        onCreatingChange?.(false)
        onChange(previousSecretId)
        setCreating(false)
      }}
      onCreated={(secret) => {
        onCreatingChange?.(false)
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
  owner,
  integrationProvider,
  onPendingChange,
  onCancel,
  onCreated,
}: {
  orgId: string
  label: string
  defaultName: string
  valuePlaceholder: string
  kind: 'generic' | 'aws_credentials' | 'integration_credentials'
  owner: SecretOwnerInput
  integrationProvider: IntegrationAppProvider
  onPendingChange?: (pending: boolean) => void
  onCancel: () => void
  onCreated: (secret: Secret) => void
}) {
  const createSecret = useCreateSecret(orgId)
  const [name, setName] = useState(defaultName)
  const [secretValue, setSecretValue] = useState('')
  const [awsCredentials, setAWSCredentials] = useState(newAWSCredentialsSecret)
  const [integrationCredentials, setIntegrationCredentials] = useState(() =>
    newIntegrationCredentials(integrationProvider),
  )
  const integrationMaterial = integrationCredentialsMaterial(integrationCredentials)
  const [error, setError] = useState('')
  const awsMaterial = awsCredentialsMaterial(awsCredentials)
  const valid =
    resourceNameValid(name) &&
    (kind === 'integration_credentials'
      ? integrationMaterial !== undefined
      : kind === 'aws_credentials'
        ? awsMaterial !== undefined
        : secretValue !== '')

  async function submit() {
    if (!valid || createSecret.isPending) return
    setError('')
    onPendingChange?.(true)
    try {
      const material =
        kind === 'integration_credentials'
          ? integrationMaterial
          : kind === 'aws_credentials'
            ? awsMaterial
            : { kind: 'generic' as const, value: secretValue }
      if (material === undefined) return
      const secret = await createSecret.mutateAsync({
        owner,
        name,
        material,
      })
      onCreated(secret)
    } catch (err) {
      setError(errorMessage(err, 'Could not create secret'))
    } finally {
      onPendingChange?.(false)
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
      <fieldset disabled={createSecret.isPending} className="grid gap-3 rounded-md border p-3">
        <div
          className={
            kind === 'integration_credentials' ? 'grid gap-3' : 'grid gap-3 sm:grid-cols-2'
          }
        >
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
          {kind === 'integration_credentials' ? (
            <IntegrationCredentialsFields
              value={integrationCredentials}
              onChange={setIntegrationCredentials}
              fixedProvider
            />
          ) : kind === 'aws_credentials' ? (
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
          <FieldDescription>
            {owner.kind === 'project'
              ? 'Stored as a secret owned by this project.'
              : 'Stored as an organization secret.'}
          </FieldDescription>
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
      </fieldset>
    </Field>
  )
}
