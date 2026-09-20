import { useConfigureProjectApp, useCreateSecret, useProjectAvailableSecrets } from '@omnara/react'
import type { ProjectApp } from '@omnara/sdk'
import { type SyntheticEvent, useEffect, useRef, useState } from 'react'
import { z } from 'zod'

import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

import { AppCredentialFields } from './ProjectAppFormCredentials'
import { projectAppFormError } from './projectAppFormState'
import { DiscordAppInteractionsSetup } from './ProjectAppProfilePicker'
import { submitProjectAppSetup } from './projectAppSetupSubmission'

export function ProjectAppSetup({
  orgId,
  projectId,
  app,
  onSaved,
  onCancel,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
  onSaved: (app: ProjectApp) => void
  onCancel: () => void
}) {
  const setup = useConfigureProjectApp(orgId, projectId)
  const createSecret = useCreateSecret(orgId)
  const [newCredential, setNewCredential] = useState(!app.credential_secret_id)
  const [savedSecret, setSavedSecret] = useState('')
  const [selectedSecret, setSelectedSecret] = useState(app.credential_secret_id ?? '')
  const [error, setError] = useState('')
  const submitting = useRef(false)
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const [busy, setBusy] = useState(false)
  const secretsQuery = useProjectAvailableSecrets(orgId, projectId, {
    filters: { kind: app.provider === 'github' ? 'github_app_credentials' : 'generic' },
    enabled: !newCredential && !savedSecret,
  })
  const secrets = useInfiniteQueryItems(secretsQuery).map((access) => access.secret)
  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (submitting.current) return
    submitting.current = true
    setBusy(true)
    setError('')
    try {
      const saved = await submitProjectAppSetup(
        { form: new FormData(event.currentTarget), projectId, app, savedSecret, newCredential },
        {
          createSecret: createSecret.mutateAsync,
          configureApp: setup.mutateAsync,
          onSecretSaved: (id) => {
            if (mounted.current) setSavedSecret(id)
          },
        },
      )
      if (mounted.current) onSaved(saved)
    } catch (cause) {
      if (mounted.current)
        setError(cause instanceof Error ? projectAppFormError(cause) : 'Could not connect app.')
    } finally {
      submitting.current = false
      if (mounted.current) setBusy(false)
    }
  }
  return (
    <form onSubmit={(event) => void submit(event)} autoComplete="off">
      <FieldGroup>
        <h2 className="font-medium">
          {app.provider_tenant_id ? 'Reconnect app' : 'Connect account'}
        </h2>
        <fieldset disabled={busy} className="flex flex-col gap-5">
          <Field>
            <FieldLabel htmlFor="provider-tenant">
              {app.provider === 'github' ? 'GitHub App ID' : 'Discord Application ID'}
            </FieldLabel>
            <Input
              id="provider-tenant"
              name="tenant"
              defaultValue={app.provider_tenant_id}
              readOnly={Boolean(app.provider_tenant_id || savedSecret)}
              required
              pattern="[1-9][0-9]*"
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="provider-account">
              {app.provider === 'github' ? 'Installation ID' : 'Bot User ID'}
            </FieldLabel>
            <Input
              id="provider-account"
              name="account"
              defaultValue={app.provider_account_ref}
              readOnly={Boolean(app.provider_account_ref)}
              required
              pattern="[1-9][0-9]*"
            />
            {app.provider_tenant_id && (
              <FieldDescription>
                Reconnect the same provider account. Create another app to use a different account.
              </FieldDescription>
            )}
          </Field>
          <Field>
            <FieldLabel htmlFor="provider-display">Bot display name (optional)</FieldLabel>
            <Input
              id="provider-display"
              name="displayName"
              defaultValue={app.provider_agent_display_name}
            />
          </Field>
          {savedSecret ? (
            <div className="flex flex-col gap-2 text-sm">
              <p>Credentials saved. Retry reuses the saved secret.</p>
              <Button
                type="button"
                variant="outline"
                onClick={() => {
                  setSavedSecret('')
                  setNewCredential(false)
                }}
              >
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
                    setNewCredential(event.target.checked)
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
                      <Button
                        type="button"
                        variant="link"
                        onClick={() => void secretsQuery.refetch()}
                      >
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
          {app.provider === 'discord' && (
            <>
              <Field>
                <FieldLabel htmlFor="discord-public-key">Interaction public key</FieldLabel>
                <Input
                  id="discord-public-key"
                  name="publicKey"
                  defaultValue={z.string().catch('').parse(app.provider_config.public_key)}
                  pattern="[a-fA-F0-9]{64}"
                  required
                />
                <FieldDescription>
                  Required for profile choices and agent questions.
                </FieldDescription>
              </Field>
              <Field>
                <FieldLabel htmlFor="discord-shards">Gateway shards</FieldLabel>
                <Input
                  id="discord-shards"
                  name="shards"
                  type="number"
                  min={1}
                  max={4096}
                  defaultValue={Number(app.provider_config.shard_count ?? 1)}
                  required
                />
              </Field>
              <DiscordAppInteractionsSetup app={app} />
            </>
          )}
        </fieldset>
        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" disabled={busy} onClick={onCancel}>
            Cancel
          </Button>
          <Button type="submit" loading={busy} disabled={busy}>
            {app.provider_tenant_id ? 'Reconnect app' : 'Connect app'}
          </Button>
        </div>
      </FieldGroup>
    </form>
  )
}
