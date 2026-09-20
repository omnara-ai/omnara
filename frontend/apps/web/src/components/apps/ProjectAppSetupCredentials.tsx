import { useProjectAvailableSecrets } from '@omnara/react'
import type { ProjectApp } from '@omnara/sdk'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

import { AppCredentialFields } from './ProjectAppFormCredentials'

export function ProjectAppSetupCredentials({
  orgId,
  projectId,
  app,
  savedSecret,
  newCredential,
  onNewCredentialChange,
  onChooseCredentials,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
  savedSecret: string
  newCredential: boolean
  onNewCredentialChange: (value: boolean) => void
  onChooseCredentials: () => void
}) {
  const [selectedSecret, setSelectedSecret] = useState(app.credential_secret_id ?? '')
  const secretsQuery = useProjectAvailableSecrets(orgId, projectId, {
    filters: { kind: app.provider === 'github' ? 'github_app_credentials' : 'generic' },
    enabled: !newCredential && !savedSecret,
  })
  const secrets = useInfiniteQueryItems(secretsQuery).map((access) => access.secret)
  return (
    <>
      {savedSecret ? (
        <div className="flex flex-col gap-2 text-sm">
          <p>Credentials saved. Retry reuses the saved secret.</p>
          <Button type="button" variant="outline" onClick={onChooseCredentials}>
            Choose different credentials
          </Button>
        </div>
      ) : (
        <>
          <label className="flex gap-2 text-sm">
            <input
              type="checkbox"
              checked={newCredential}
              onChange={(event) => {
                onNewCredentialChange(event.target.checked)
              }}
            />
            Create a new credential
          </label>
          {newCredential ? (
            <>
              <Field>
                <FieldLabel htmlFor="credential-name">Credential name</FieldLabel>
                <Input
                  id="credential-name"
                  name="secretName"
                  defaultValue={`${app.name}-credentials`}
                  required
                />
              </Field>
              <AppCredentialFields provider={app.provider} />
            </>
          ) : (
            <Field>
              <FieldLabel htmlFor="saved-secret">Saved credential</FieldLabel>
              <select
                id="saved-secret"
                aria-label="Saved credential"
                name="secret"
                required
                value={selectedSecret}
                onChange={(event) => {
                  setSelectedSecret(event.target.value)
                }}
                className="border-input bg-background h-9 rounded-md border px-3 text-sm"
              >
                <option value="">Choose a credential</option>
                {app.credential_secret_id &&
                  !secrets.some((secret) => secret.id === app.credential_secret_id) && (
                    <option value={app.credential_secret_id}>Current credential</option>
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
                  disabled={secretsQuery.isFetchingNextPage}
                  onClick={() => void secretsQuery.fetchNextPage()}
                >
                  More credentials
                </Button>
              )}
            </Field>
          )}
        </>
      )}
    </>
  )
}
