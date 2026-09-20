import { useConfigureProjectApp, useCreateSecret } from '@omnara/react'
import type { ProjectApp } from '@omnara/sdk'
import { type SyntheticEvent, useEffect, useRef, useState } from 'react'
import { z } from 'zod'

import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { projectAppFormError } from './projectAppFormState'
import { DiscordAppInteractionsSetup } from './ProjectAppProfilePicker'
import { ProjectAppSetupCredentials } from './ProjectAppSetupCredentials'
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
    }
    submitting.current = false
    if (mounted.current) setBusy(false)
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
          <ProjectAppSetupCredentials
            orgId={orgId}
            projectId={projectId}
            app={app}
            savedSecret={savedSecret}
            newCredential={newCredential}
            onNewCredentialChange={setNewCredential}
            onChooseCredentials={() => {
              setSavedSecret('')
              setNewCredential(false)
            }}
          />
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
