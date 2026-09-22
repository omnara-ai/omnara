import { useConfigureProjectApp, useCreateSecret } from '@omnara/react'
import type { AppType, ProjectApp } from '@omnara/sdk'
import { type ReactNode, type SyntheticEvent, useEffect, useRef, useState } from 'react'
import { z } from 'zod'

import { Button } from '@/components/ui/button'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { projectAppFormError } from './projectAppFormState'
import { ProjectAppNameField } from './ProjectAppNameField'
import { ProjectAppPortalSetup } from './ProjectAppPortalSetup'
import { ProjectAppSetupCredentials } from './ProjectAppSetupCredentials'
import { ProjectAppSetupGroup } from './ProjectAppSetupGroup'
import { submitProjectAppSetup } from './projectAppSetupSubmission'
import { useProjectAppDraft } from './useProjectAppDraft'

export function ProjectAppSetup({
  orgId,
  projectId,
  app: existing,
  appType,
  onSaved,
  onCancel,
  footerAction,
}: {
  orgId: string
  projectId: string
  app?: ProjectApp
  appType: Exclude<AppType, 'slack_thread'>
  onSaved: (app: ProjectApp) => void
  onCancel?: () => void
  footerAction?: ReactNode
}) {
  const { app, name, setName, ensureApp } = useProjectAppDraft(orgId, projectId, appType, existing)
  const setup = useConfigureProjectApp(orgId, projectId)
  const createSecret = useCreateSecret(orgId)
  const [newCredential, setNewCredential] = useState(!existing?.credential_secret_id)
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
  const [tenant, setTenant] = useState(existing?.provider_tenant_id ?? '')
  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (submitting.current) return
    submitting.current = true
    setBusy(true)
    setError('')
    const form = new FormData(event.currentTarget)
    try {
      const draft = await ensureApp()
      const saved = await submitProjectAppSetup(
        { form, projectId, app: draft, savedSecret, newCredential },
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
  const github = appType === 'github_pr'
  const provider = github ? 'GitHub' : 'Discord'
  const providerTenant = app?.provider_tenant_id ?? ''
  const reconnect = Boolean(providerTenant)
  return (
    <form onSubmit={(event) => void submit(event)} autoComplete="off">
      <FieldGroup className="gap-8 text-sm">
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
                Open your application in the{' '}
                <a
                  href="https://discord.com/developers/applications"
                  target="_blank"
                  rel="noreferrer"
                  className="text-foreground underline underline-offset-2"
                >
                  Discord Developer Portal
                </a>
                . Enter the details below and connect. You’ll then get the steps to finish setup in
                Discord.
              </>
            )}
          </p>
        </div>
        <fieldset disabled={busy} className="flex flex-col gap-8">
          {!existing && (
            <ProjectAppSetupGroup
              title="Name in Omnara"
              hint="A permanent name for this app in your project."
            >
              <ProjectAppNameField name={name} onChange={setName} saved={app} />
            </ProjectAppSetupGroup>
          )}
          <ProjectAppSetupGroup
            title={github ? 'App identity' : '1. Application details'}
            hint={
              github
                ? 'The App ID is on the app’s settings page. The Installation ID is a different number, at the end of its installation URL.'
                : 'Copy both values from General Information.'
            }
          >
            <div className="grid gap-4 sm:grid-cols-2">
              <Field>
                <FieldLabel htmlFor="provider-tenant">
                  {github ? 'GitHub App ID' : 'Discord Application ID'}
                </FieldLabel>
                <Input
                  key={app?.provider_tenant_id ?? ''}
                  id="provider-tenant"
                  name="tenant"
                  inputMode="numeric"
                  defaultValue={app?.provider_tenant_id}
                  readOnly={Boolean(providerTenant || savedSecret)}
                  required
                  pattern="[1-9][0-9]*"
                  onChange={(event) => {
                    setTenant(event.target.value.trim())
                  }}
                />
              </Field>
              {github && (
                <Field>
                  <FieldLabel htmlFor="provider-account">Installation ID</FieldLabel>
                  <Input
                    key={app?.provider_account_ref ?? ''}
                    id="provider-account"
                    name="account"
                    inputMode="numeric"
                    defaultValue={app?.provider_account_ref}
                    readOnly={Boolean(app?.provider_account_ref)}
                    required
                    pattern="[1-9][0-9]*"
                  />
                </Field>
              )}
              {!github && (
                <Field>
                  <FieldLabel htmlFor="discord-public-key">Public key</FieldLabel>
                  <Input
                    id="discord-public-key"
                    name="publicKey"
                    className="font-mono"
                    spellCheck={false}
                    defaultValue={z.string().catch('').parse(app?.provider_config.public_key)}
                    pattern="[a-fA-F0-9]{64}"
                    required
                  />
                </Field>
              )}
            </div>
            {github && (
              <Field>
                <FieldLabel htmlFor="provider-display">Bot display name (optional)</FieldLabel>
                <Input
                  id="provider-display"
                  name="displayName"
                  defaultValue={app?.provider_agent_display_name}
                />
              </Field>
            )}
          </ProjectAppSetupGroup>
          <ProjectAppSetupGroup
            title={github ? 'Credentials' : '2. Bot token'}
            hint={
              github
                ? 'The private key lets Omnara act as the app. The webhook secret verifies what GitHub sends.'
                : 'On the Bot page, copy your token (or use Reset Token to create one). Enable Message Content Intent so agents can read replies.'
            }
          >
            <ProjectAppSetupCredentials
              orgId={orgId}
              projectId={projectId}
              appType={appType}
              name={name}
              credentialSecretId={app?.credential_secret_id}
              savedSecret={savedSecret}
              newCredential={newCredential}
              onNewCredentialChange={setNewCredential}
              onChooseCredentials={() => {
                setSavedSecret('')
                setNewCredential(false)
              }}
            />
          </ProjectAppSetupGroup>
          {github && (
            <ProjectAppSetupGroup
              title="Then, in GitHub"
              hint="Connect here first, then finish setup in your GitHub App’s settings."
            >
              <ProjectAppPortalSetup appType="github_pr" providerId={providerTenant || tenant} />
            </ProjectAppSetupGroup>
          )}
        </fieldset>
        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
        <fieldset disabled={busy} className="flex flex-wrap items-start justify-end gap-2">
          {footerAction}
          {onCancel && (
            <Button type="button" variant="outline" disabled={busy} onClick={onCancel}>
              Cancel
            </Button>
          )}
          <Button type="submit" loading={busy} disabled={busy}>
            {!existing ? 'Create and connect' : reconnect ? 'Reconnect app' : 'Connect app'}
          </Button>
        </fieldset>
      </FieldGroup>
    </form>
  )
}
