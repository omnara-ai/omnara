import { useProjectAvailableSecrets } from '@omnara/react'
import type { ProjectApp } from '@omnara/sdk'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { CheckboxField, Field, FieldLabel } from '@/components/ui/field'
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
    filters: { kind: app.app_type === 'github_pr' ? 'github_app_credentials' : 'generic' },
    enabled: !newCredential && !savedSecret,
  })
  const secrets = useInfiniteQueryItems(secretsQuery).map((access) => access.secret)
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
                  defaultValue={`${app.name}-credentials`}
                  required
                />
              </Field>
              <AppCredentialFields appType={app.app_type} />
            </div>
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
                className="control-focus rounded-control border-input bg-card h-10 w-full border px-3 text-sm"
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
                  size="sm"
                  className="self-start"
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
