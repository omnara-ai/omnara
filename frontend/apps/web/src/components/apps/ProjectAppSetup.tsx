import { useConfigureProjectApp, useCreateSecret } from '@omnara/react'
import type { ProjectApp } from '@omnara/sdk'
import { type ReactNode, type SyntheticEvent, useEffect, useId, useRef, useState } from 'react'
import { z } from 'zod'

import { ChevronRightIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { projectAppFormError } from './projectAppFormState'
import { ProjectAppPortalSetup } from './ProjectAppPortalSetup'
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
  onCancel?: () => void
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
  // Mirrors the typed provider ID so the portal URL is only offered once it is real.
  const [tenant, setTenant] = useState(app.provider_tenant_id)
  const shards = Number(app.provider_config.shard_count ?? 1)
  const [moreOpen] = useState(shards !== 1)
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
  const github = app.app_type === 'github_pr'
  const provider = github ? 'GitHub' : 'Discord'
  const reconnect = Boolean(app.provider_tenant_id)
  return (
    <form onSubmit={(event) => void submit(event)} autoComplete="off">
      <FieldGroup className="gap-8">
        <div className="flex flex-col gap-2">
          <h2 className="font-medium">
            {reconnect ? 'Reconnect' : 'Connect'} {provider}
          </h2>
          <p className="text-muted-foreground">
            {reconnect ? (
              'Reconnect the same provider account. Create another app to use a different account.'
            ) : github ? (
              'Copy these values from your GitHub App’s settings. Next, you’ll choose the agent to launch for pull requests.'
            ) : (
              <>
                Copy these values from your application in the{' '}
                <a
                  href="https://discord.com/developers/applications"
                  target="_blank"
                  rel="noreferrer"
                  className="text-foreground underline underline-offset-2"
                >
                  Discord Developer Portal
                </a>
                . Next, you’ll choose which agents people can start.
              </>
            )}
          </p>
        </div>
        <fieldset disabled={busy} className="flex flex-col gap-8">
          <SetupGroup
            title={github ? 'App identity' : 'Bot identity'}
            hint={
              github
                ? 'The App ID is on the app’s settings page. The Installation ID is a different number, at the end of its installation URL.'
                : 'Find the Application ID under General Information. Copy the User ID from the bot’s profile in Discord.'
            }
          >
            <div className="grid gap-4 sm:grid-cols-2">
              <Field>
                <FieldLabel htmlFor="provider-tenant">
                  {github ? 'GitHub App ID' : 'Discord Application ID'}
                </FieldLabel>
                <Input
                  key={app.provider_tenant_id}
                  id="provider-tenant"
                  name="tenant"
                  inputMode="numeric"
                  defaultValue={app.provider_tenant_id}
                  readOnly={Boolean(app.provider_tenant_id || savedSecret)}
                  required
                  pattern="[1-9][0-9]*"
                  onChange={(event) => {
                    setTenant(event.target.value.trim())
                  }}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="provider-account">
                  {github ? 'Installation ID' : 'Bot User ID'}
                </FieldLabel>
                <Input
                  key={app.provider_account_ref}
                  id="provider-account"
                  name="account"
                  inputMode="numeric"
                  defaultValue={app.provider_account_ref}
                  readOnly={Boolean(app.provider_account_ref)}
                  required
                  pattern="[1-9][0-9]*"
                />
              </Field>
            </div>
          </SetupGroup>
          <SetupGroup
            title="Credentials"
            hint={
              github
                ? 'The private key lets Omnara act as the app. The webhook secret verifies what GitHub sends.'
                : 'Use the token from the Bot page and the public key from General Information.'
            }
          >
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
            {!github && (
              <Field>
                <FieldLabel htmlFor="discord-public-key">Interaction public key</FieldLabel>
                <Input
                  id="discord-public-key"
                  name="publicKey"
                  className="font-mono"
                  spellCheck={false}
                  defaultValue={z.string().catch('').parse(app.provider_config.public_key)}
                  pattern="[a-fA-F0-9]{64}"
                  required
                />
                <FieldDescription>
                  Lets Omnara verify profile choices and answers to agent questions.
                </FieldDescription>
              </Field>
            )}
          </SetupGroup>
          <SetupGroup
            title={`Then, in ${provider}`}
            hint={
              github
                ? 'Connect here first, then finish setup in your GitHub App’s settings.'
                : 'Connect here first, then save this URL in the Discord Developer Portal.'
            }
          >
            <ProjectAppPortalSetup
              appType={github ? 'github_pr' : 'discord_thread'}
              providerId={app.provider_tenant_id || tenant}
            />
          </SetupGroup>
          <details
            className="group border-t pt-6"
            open={moreOpen || undefined}
            onInvalid={(event) => {
              // A closed disclosure cannot focus its invalid field, so the browser would stay silent.
              event.currentTarget.open = true
            }}
          >
            <summary className="text-muted-foreground hover:text-foreground focus-visible:ring-ring flex w-fit cursor-pointer list-none items-center gap-1.5 rounded-sm outline-none focus-visible:ring-2 [&::-webkit-details-marker]:hidden">
              <ChevronRightIcon className="size-4 shrink-0 transition-transform group-open:rotate-90" />
              More options
            </summary>
            <div className="grid gap-4 pt-5 sm:grid-cols-2 lg:pl-[15rem]">
              <Field>
                <FieldLabel htmlFor="provider-display">Bot display name (optional)</FieldLabel>
                <Input
                  id="provider-display"
                  name="displayName"
                  defaultValue={app.provider_agent_display_name}
                />
              </Field>
              {!github && (
                <Field>
                  <FieldLabel htmlFor="discord-shards">Gateway shards</FieldLabel>
                  <Input
                    id="discord-shards"
                    name="shards"
                    type="number"
                    min={1}
                    max={4096}
                    defaultValue={shards}
                    required
                  />
                  <FieldDescription>
                    Keep 1 unless Discord requires more shards for this bot.
                  </FieldDescription>
                </Field>
              )}
            </div>
          </details>
        </fieldset>
        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          {onCancel && (
            <Button type="button" variant="outline" disabled={busy} onClick={onCancel}>
              Cancel
            </Button>
          )}
          <Button type="submit" loading={busy} disabled={busy}>
            {reconnect ? 'Reconnect app' : 'Connect app'}
          </Button>
        </div>
      </FieldGroup>
    </form>
  )
}

/** One task in the form: what it is and where to find it beside the fields that answer it. */
function SetupGroup({
  title,
  hint,
  children,
}: {
  title: string
  hint: string
  children: ReactNode
}) {
  const id = useId()
  return (
    <div
      role="group"
      aria-labelledby={id}
      className="grid gap-4 border-t pt-6 first:border-t-0 first:pt-0 lg:grid-cols-[13rem_1fr] lg:gap-8"
    >
      <div className="flex flex-col gap-1.5">
        <h3 id={id} className="font-medium">
          {title}
        </h3>
        <p className="text-muted-foreground">{hint}</p>
      </div>
      <div className="flex min-w-0 flex-col gap-5">{children}</div>
    </div>
  )
}
