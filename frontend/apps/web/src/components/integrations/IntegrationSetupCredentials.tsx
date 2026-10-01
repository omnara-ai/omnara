import { useProjectAvailableSecrets } from '@omnara/react'
import type { IntegrationKind } from '@omnara/sdk'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { CheckboxField, Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

import { IntegrationCredentialFields } from './IntegrationFormCredentials'

export function IntegrationSetupCredentials({
  orgId,
  projectId,
  integrationKind,
  name,
  credentialSecretId,
  savedSecret,
  selectedSecret,
  onSelectedSecretChange,
  newCredential,
  onNewCredentialChange,
  onChooseCredentials,
  disabled,
}: {
  orgId: string
  projectId: string
  integrationKind: IntegrationKind
  name: string
  credentialSecretId?: string
  savedSecret: string
  selectedSecret: string
  onSelectedSecretChange: (value: string) => void
  newCredential: boolean
  onNewCredentialChange: (value: boolean) => void
  onChooseCredentials: () => void
  disabled: boolean
}) {
  const [credentialName, setCredentialName] = useState<string>()
  return (
    <>
      {savedSecret ? (
        <div className="flex flex-col items-start gap-2 text-sm">
          <p>Credentials saved. Retry reuses the saved secret.</p>
          <Button type="button" variant="outline" size="sm" onClick={onChooseCredentials}>
            Choose different credentials
          </Button>
        </div>
      ) : (
        <>
          <CheckboxField
            className="items-start"
            inputClassName="mt-0.5"
            label="Create a new credential"
            description="Uncheck to use a saved project credential."
            checked={newCredential}
            onChange={(event) => {
              onNewCredentialChange(event.target.checked)
            }}
          />
          {newCredential ? (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field>
                <FieldLabel htmlFor="credential-name">Credential name</FieldLabel>
                <Input
                  id="credential-name"
                  name="secretName"
                  value={credentialName ?? `${name}-credentials`}
                  onChange={(event) => {
                    setCredentialName(event.target.value)
                  }}
                  required
                />
              </Field>
              <IntegrationCredentialFields integrationKind={integrationKind} />
            </div>
          ) : (
            <IntegrationCredentialPicker
              orgId={orgId}
              projectId={projectId}
              integrationKind={integrationKind}
              value={selectedSecret}
              onChange={onSelectedSecretChange}
              currentCredentialId={credentialSecretId}
              disabled={disabled}
            />
          )}
        </>
      )}
    </>
  )
}

export function IntegrationCredentialPicker({
  orgId,
  projectId,
  integrationKind,
  value,
  onChange,
  currentCredentialId,
  disabled,
}: {
  orgId: string
  projectId: string
  integrationKind: IntegrationKind
  value: string
  onChange: (value: string) => void
  currentCredentialId?: string
  disabled: boolean
}) {
  const secretsQuery = useProjectAvailableSecrets(orgId, projectId, {
    filters: { kind: integrationKind === 'github_pr' ? 'github_app_credentials' : 'generic' },
  })
  const secrets = useInfiniteQueryItems(secretsQuery).map((access) => access.secret)
  const saved = secrets.find((secret) => secret.id === value)
  const fallback = value === currentCredentialId ? 'Current credential' : value
  return (
    <Field>
      <FieldLabel htmlFor="saved-secret">Saved credential</FieldLabel>
      <Select name="secret" required value={value} disabled={disabled} onValueChange={onChange}>
        <SelectTrigger id="saved-secret" className="w-full">
          <SelectValue placeholder="Choose a credential">{saved?.name ?? fallback}</SelectValue>
        </SelectTrigger>
        <SelectContent>
          {value && !saved && (
            <SelectItem value={value} disabled={disabled}>
              {fallback}
            </SelectItem>
          )}
          {secrets.map((secret) => (
            <SelectItem key={secret.id} value={secret.id} disabled={disabled}>
              {secret.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {secretsQuery.isError && (
        <div role="alert">
          Could not load credentials.{' '}
          <Button type="button" variant="link" onClick={() => void secretsQuery.refetch()}>
            Retry credentials
          </Button>
        </div>
      )}
      {secretsQuery.hasNextPage && (
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="self-start"
          disabled={secretsQuery.isFetchingNextPage}
          onClick={() => void secretsQuery.fetchNextPage()}
        >
          More credentials
        </Button>
      )}
    </Field>
  )
}
