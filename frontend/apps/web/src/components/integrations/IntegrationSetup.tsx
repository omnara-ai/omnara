import { useConfigureIntegration, useCreateSecret } from '@omnara/react'
import type { Integration, IntegrationKind } from '@omnara/sdk'
import { type ReactNode, type SyntheticEvent, useEffect, useRef } from 'react'

import { Button } from '@/components/ui/button'
import { FieldGroup } from '@/components/ui/field'

import { integrationFormError } from './integrationFormState'
import { IntegrationNameField } from './IntegrationNameField'
import { IntegrationSetupCredentials } from './IntegrationSetupCredentials'
import { DiscordSetupDetails, GitHubSetupDetails } from './IntegrationSetupDetails'
import { submitIntegrationSetup } from './integrationSetupSubmission'
import { useIntegrationDraft } from './useIntegrationDraft'
import { useIntegrationSetupState } from './useIntegrationSetupState'

interface IntegrationSetupProps {
  orgId: string
  projectId: string
  integration?: Integration
  integrationKind: Exclude<IntegrationKind, 'slack_thread'>
  onSaved: (integration: Integration) => void
  onCancel?: () => void
  footerAction?: ReactNode
  disabled?: boolean
}

export function IntegrationSetup(props: IntegrationSetupProps) {
  const draft = useIntegrationDraft(
    props.orgId,
    props.projectId,
    props.integrationKind,
    props.integration,
  )
  const state = useIntegrationSetupState(props.integration)
  return <IntegrationSetupForm {...props} draft={draft} state={state} />
}

export function IntegrationSetupForm({
  orgId,
  projectId,
  integration: existing,
  integrationKind,
  onSaved,
  onCancel,
  footerAction,
  disabled = false,
  draft,
  state,
}: IntegrationSetupProps & {
  draft: ReturnType<typeof useIntegrationDraft>
  state: ReturnType<typeof useIntegrationSetupState>
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
  const configure = useConfigureIntegration(orgId, projectId)
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
        const saved = await submitIntegrationSetup(
          { form, projectId, integration: await ensureIntegration(), savedSecret, newCredential },
          {
            createSecret: createSecret.mutateAsync,
            configureIntegration: configure.mutateAsync,
            onSecretSaved: setSavedSecret,
          },
        )
        if (mounted.current) onSaved(saved)
      },
      (cause) => integrationFormError(cause, 'Could not connect integration.'),
    )
  }
  const github = integrationKind === 'github_pr'
  const provider = github ? 'GitHub' : 'Discord'
  const reconnect = Boolean(integration?.provider_tenant_id)
  const credentials = (
    <IntegrationSetupCredentials
      orgId={orgId}
      projectId={projectId}
      integrationKind={integrationKind}
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
      disabled={busy || disabled}
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
              'Copy your GitHub App’s details from its settings, then choose an agent for pull requests.'
            ) : (
              <>
                Enter your application’s details from the{' '}
                <a
                  href="https://discord.com/developers/applications"
                  target="_blank"
                  rel="noreferrer"
                  className="text-foreground underline underline-offset-2"
                >
                  Discord Developer Portal
                </a>
                . After connecting, you’ll get the steps to finish setup in Discord.
              </>
            )}
          </p>
        </div>
        <fieldset disabled={busy} className="flex flex-col gap-8">
          {!existing && <IntegrationNameField name={name} onChange={setName} saved={integration} />}
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
