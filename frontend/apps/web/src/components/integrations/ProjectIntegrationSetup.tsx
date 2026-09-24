import { useConfigureProjectIntegration, useCreateSecret } from '@omnara/react'
import type { IntegrationType, ProjectIntegration } from '@omnara/sdk'
import { type ReactNode, type SyntheticEvent, useEffect, useRef } from 'react'

import { Button } from '@/components/ui/button'
import { FieldGroup } from '@/components/ui/field'

import { projectIntegrationFormError } from './projectIntegrationFormState'
import { ProjectIntegrationNameField } from './ProjectIntegrationNameField'
import { ProjectIntegrationSetupCredentials } from './ProjectIntegrationSetupCredentials'
import { DiscordSetupDetails, GitHubSetupDetails } from './ProjectIntegrationSetupDetails'
import { ProjectIntegrationSetupGroup } from './ProjectIntegrationSetupGroup'
import { submitProjectIntegrationSetup } from './projectIntegrationSetupSubmission'
import { useProjectIntegrationDraft } from './useProjectIntegrationDraft'
import { useProjectIntegrationSetupState } from './useProjectIntegrationSetupState'

interface ProjectIntegrationSetupProps {
  orgId: string
  projectId: string
  integration?: ProjectIntegration
  integrationType: Exclude<IntegrationType, 'slack_thread'>
  onSaved: (integration: ProjectIntegration) => void
  onCancel?: () => void
  footerAction?: ReactNode
}

export function ProjectIntegrationSetup(props: ProjectIntegrationSetupProps) {
  const draft = useProjectIntegrationDraft(
    props.orgId,
    props.projectId,
    props.integrationType,
    props.integration,
  )
  const state = useProjectIntegrationSetupState(props.integration)
  return <ProjectIntegrationSetupForm {...props} draft={draft} state={state} />
}

export function ProjectIntegrationSetupForm({
  orgId,
  projectId,
  integration: existing,
  integrationType,
  onSaved,
  onCancel,
  footerAction,
  draft,
  state,
}: ProjectIntegrationSetupProps & {
  draft: ReturnType<typeof useProjectIntegrationDraft>
  state: ReturnType<typeof useProjectIntegrationSetupState>
}) {
  const { integration, name, setName, ensureIntegration } = draft
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
  const configure = useConfigureProjectIntegration(orgId, projectId)
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
        const saved = await submitProjectIntegrationSetup(
          { form, projectId, integration: await ensureIntegration(), savedSecret, newCredential },
          {
            createSecret: createSecret.mutateAsync,
            configureIntegration: configure.mutateAsync,
            onSecretSaved: setSavedSecret,
          },
        )
        if (mounted.current) onSaved(saved)
      },
      (cause) => projectIntegrationFormError(cause, 'Could not connect integration.'),
    )
  }
  const github = integrationType === 'github_pr'
  const provider = github ? 'GitHub' : 'Discord'
  const reconnect = Boolean(integration?.provider_tenant_id)
  const credentials = (
    <ProjectIntegrationSetupCredentials
      orgId={orgId}
      projectId={projectId}
      integrationType={integrationType}
      name={name}
      credentialSecretId={integration?.credential_secret_id}
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
              'Reconnect the same provider account. Create another integration to use a different account.'
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
            <ProjectIntegrationSetupGroup
              title="Name in Omnara"
              hint="A permanent name for this integration in your project."
            >
              <ProjectIntegrationNameField name={name} onChange={setName} saved={integration} />
            </ProjectIntegrationSetupGroup>
          )}
          {github ? (
            <GitHubSetupDetails
              integration={integration}
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
              integration={integration}
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
            {!existing
              ? 'Create and connect'
              : reconnect
                ? 'Reconnect integration'
                : 'Connect integration'}
          </Button>
        </fieldset>
      </FieldGroup>
    </form>
  )
}
