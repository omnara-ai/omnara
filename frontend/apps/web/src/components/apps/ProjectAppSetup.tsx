import { useConfigureProjectApp, useCreateSecret } from '@omnara/react'
import type { AppType, ProjectApp } from '@omnara/sdk'
import { type ReactNode, type SyntheticEvent, useEffect, useRef } from 'react'

import { Button } from '@/components/ui/button'
import { FieldGroup } from '@/components/ui/field'

import { projectAppFormError } from './projectAppFormState'
import { ProjectAppNameField } from './ProjectAppNameField'
import { ProjectAppSetupCredentials } from './ProjectAppSetupCredentials'
import { DiscordSetupDetails, GitHubSetupDetails } from './ProjectAppSetupDetails'
import { ProjectAppSetupGroup } from './ProjectAppSetupGroup'
import { submitProjectAppSetup } from './projectAppSetupSubmission'
import { useProjectAppDraft } from './useProjectAppDraft'
import { useProjectAppSetupState } from './useProjectAppSetupState'

interface ProjectAppSetupProps {
  orgId: string
  projectId: string
  app?: ProjectApp
  appType: Exclude<AppType, 'slack_thread'>
  onSaved: (app: ProjectApp) => void
  onCancel?: () => void
  footerAction?: ReactNode
}

export function ProjectAppSetup(props: ProjectAppSetupProps) {
  const draft = useProjectAppDraft(props.orgId, props.projectId, props.appType, props.app)
  const state = useProjectAppSetupState(props.app)
  return <ProjectAppSetupForm {...props} draft={draft} state={state} />
}

export function ProjectAppSetupForm({
  orgId,
  projectId,
  app: existing,
  appType,
  onSaved,
  onCancel,
  footerAction,
  draft,
  state,
}: ProjectAppSetupProps & {
  draft: ReturnType<typeof useProjectAppDraft>
  state: ReturnType<typeof useProjectAppSetupState>
}) {
  const { app, name, setName, ensureApp } = draft
  const {
    newCredential,
    setNewCredential,
    savedSecret,
    setSavedSecret,
    selectedSecret,
    setSelectedSecret,
    tenant,
    setTenant,
    account,
    setAccount,
    error,
    busy,
    run,
  } = state
  const configure = useConfigureProjectApp(orgId, projectId)
  const createSecret = useCreateSecret(orgId)
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    void run(
      async () => {
        const saved = await submitProjectAppSetup(
          { form, projectId, app: await ensureApp(), savedSecret, newCredential },
          {
            createSecret: createSecret.mutateAsync,
            configureApp: configure.mutateAsync,
            onSecretSaved: setSavedSecret,
          },
        )
        if (mounted.current) onSaved(saved)
      },
      (cause) => projectAppFormError(cause, 'Could not connect app.'),
    )
  }
  const github = appType === 'github_pr'
  const provider = github ? 'GitHub' : 'Discord'
  const reconnect = Boolean(app?.provider_tenant_id)
  const credentials = (
    <ProjectAppSetupCredentials
      orgId={orgId}
      projectId={projectId}
      appType={appType}
      name={name}
      credentialSecretId={app?.credential_secret_id}
      savedSecret={savedSecret}
      selectedSecret={selectedSecret}
      onSelectedSecretChange={setSelectedSecret}
      newCredential={newCredential}
      onNewCredentialChange={setNewCredential}
      onChooseCredentials={() => {
        setSavedSecret('')
        setNewCredential(false)
      }}
    />
  )
  return (
    <form onSubmit={submit} autoComplete="off">
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
          {github ? (
            <GitHubSetupDetails
              app={app}
              tenant={tenant}
              onTenantChange={setTenant}
              account={account}
              onAccountChange={setAccount}
              savedSecret={savedSecret}
            >
              {credentials}
            </GitHubSetupDetails>
          ) : (
            <DiscordSetupDetails
              app={app}
              tenant={tenant}
              onTenantChange={setTenant}
              savedSecret={savedSecret}
            >
              {credentials}
            </DiscordSetupDetails>
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
