import { useProjectAvailableSecrets } from '@omnara/react'
import type { AppType } from '@omnara/sdk'

import { Button } from '@/components/ui/button'
import { CheckboxField, Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

import { AppCredentialFields } from './ProjectAppFormCredentials'

export function ProjectAppSetupCredentials({
  orgId,
  projectId,
  appType,
  name,
  credentialSecretId,
  savedSecret,
  selectedSecret,
  onSelectedSecretChange,
  newCredential,
  onNewCredentialChange,
  onChooseCredentials,
}: {
  orgId: string
  projectId: string
  appType: AppType
  name: string
  credentialSecretId?: string
  savedSecret: string
  selectedSecret: string
  onSelectedSecretChange: (value: string) => void
  newCredential: boolean
  onNewCredentialChange: (value: boolean) => void
  onChooseCredentials: () => void
}) {
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
                  defaultValue={`${name}-credentials`}
                  required
                />
              </Field>
              <AppCredentialFields appType={appType} />
            </div>
          ) : (
            <ProjectAppCredentialPicker
              orgId={orgId}
              projectId={projectId}
              appType={appType}
              value={selectedSecret}
              onChange={onSelectedSecretChange}
              currentCredentialId={credentialSecretId}
            />
          )}
        </>
      )}
    </>
  )
}

/** Selects an available credential by public ID; secret material never enters the form. */
export function ProjectAppCredentialPicker({
  orgId,
  projectId,
  appType,
  value,
  onChange,
  currentCredentialId,
}: {
  orgId: string
  projectId: string
  appType: AppType
  value: string
  onChange: (value: string) => void
  currentCredentialId?: string
}) {
  const secretsQuery = useProjectAvailableSecrets(orgId, projectId, {
    filters: { kind: appType === 'github_pr' ? 'github_app_credentials' : 'generic' },
  })
  const secrets = useInfiniteQueryItems(secretsQuery).map((access) => access.secret)
  return (
    <Field>
      <FieldLabel htmlFor="saved-secret">Saved credential</FieldLabel>
      <select
        id="saved-secret"
        aria-label="Saved credential"
        name="secret"
        required
        value={value}
        onChange={(event) => {
          onChange(event.target.value)
        }}
        className="control-focus rounded-control border-input bg-card h-10 w-full border px-3 text-sm"
      >
        <option value="">Choose a credential</option>
        {value && !secrets.some((secret) => secret.id === value) && (
          <option value={value}>
            {value === currentCredentialId ? 'Current credential' : value}
          </option>
        )}
        {secrets.map((secret) => (
          <option key={secret.id} value={secret.id}>
            {secret.name}
          </option>
        ))}
      </select>
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
