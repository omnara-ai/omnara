import {
  useCreateIntegrationConnection,
  useCreateProjectApp,
  useCreateSecret,
  useProjectAvailableSecrets,
  useUpdateProjectApp,
} from '@omnara/react'
import { type IntegrationConnection, type ProfileAppProvider, type ProjectApp } from '@omnara/sdk'
import { type SyntheticEvent, useEffect, useRef, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { settleSubmission } from '@/lib/submit-status'

import { NewAppConnectionFields, ProjectAppConnectionPicker } from './ProjectAppFormConnection'
import { ProjectAppCapabilityFields, ProjectAppLauncherFields } from './ProjectAppFormFields'
import { ProjectAppFormFooter } from './ProjectAppFormFooter'
import {
  projectAppFormError,
  type ProjectAppFormValues,
  projectAppFormValues,
  validateProjectAppForm,
} from './projectAppFormState'
import { DiscordAppInteractionsSetup } from './ProjectAppProfilePicker'
import { submitProjectAppSetup } from './projectAppSetupSubmission'
import { useProjectAppFormConnection } from './useProjectAppFormConnection'

const providerLabels = { slack: 'Slack', github: 'GitHub', discord: 'Discord' }

export interface ProjectAppFormProps {
  orgId: string
  projectId: string
  provider: ProfileAppProvider
  app?: ProjectApp
  initialConnectionId?: string
  onSaved: (app: ProjectApp) => void
  onConnectSlack?: () => void
  onCancel?: () => void
}

export function ProjectAppForm(props: ProjectAppFormProps) {
  const [base, setBase] = useState(props.app)
  const stale = Boolean(
    base && props.app && (base.id !== props.app.id || base.updated_at !== props.app.updated_at),
  )
  return (
    <ProjectAppFormEditor
      key={base ? `${base.id}/${base.updated_at}` : 'create'}
      {...props}
      app={base}
      stale={stale}
      onReload={() => {
        setBase(props.app)
      }}
    />
  )
}

function ProjectAppFormEditor({
  orgId,
  projectId,
  provider,
  app,
  initialConnectionId,
  onSaved,
  onConnectSlack,
  onCancel,
  stale,
  onReload,
}: ProjectAppFormProps & { stale: boolean; onReload: () => void }) {
  const [savedConnection, setSavedConnection] = useState<IntegrationConnection>()
  const connectionLookup = useProjectAppFormConnection({
    orgId,
    projectId,
    provider,
    app,
    initialConnectionId,
    savedConnection,
  })
  const { connection, creating, settingsReady, invalidConnection } = connectionLookup
  const secretsQuery = useProjectAvailableSecrets(orgId, projectId, {
    enabled: creating,
    filters: { kind: provider === 'github' ? 'github_app_credentials' : 'generic' },
  })
  const createSecret = useCreateSecret(orgId)
  const createConnection = useCreateIntegrationConnection(orgId, projectId)
  const createApp = useCreateProjectApp(orgId, projectId)
  const updateApp = useUpdateProjectApp(orgId, projectId)
  const [newCredential, setNewCredential] = useState(true)
  const [savedSecret, setSavedSecret] = useState('')
  const [busy, setBusy] = useState(false)
  const submitting = useRef(false)
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const [error, setError] = useState('')
  const [values, setValues] = useState(() => projectAppFormValues(provider, app))
  function change(patch: Partial<ProjectAppFormValues>) {
    setValues((previous) => ({ ...previous, ...patch }))
  }
  const {
    error: validationError,
    keyStatus,
    slotCount,
  } = validateProjectAppForm(provider, values, connection, app)
  const missingSavedKey = !creating && keyStatus.missing
  const fieldsDisabled = busy || stale
  const cannotSubmit = missingSavedKey || stale || invalidConnection || Boolean(validationError)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (submitting.current || cannotSubmit) return
    submitting.current = true
    setError('')
    setBusy(true)
    const form = new FormData(event.currentTarget)
    const result = await settleSubmission(() =>
      submitProjectAppSetup(
        {
          form,
          projectId,
          provider,
          values,
          app,
          connection,
          creating,
          savedSecret,
          newCredential,
        },
        {
          createSecret: createSecret.mutateAsync,
          createConnection: createConnection.mutateAsync,
          createApp: createApp.mutateAsync,
          updateApp: updateApp.mutateAsync,
          onSecretSaved: setSavedSecret,
          onConnectionSaved: setSavedConnection,
        },
      ),
    )
    if (!mounted.current) return
    submitting.current = false
    setBusy(false)
    if (result.ok) onSaved(result.value)
    else
      setError(
        result.error instanceof Error ? projectAppFormError(result.error) : 'Could not save app.',
      )
  }

  return (
    <form onSubmit={(event) => void submit(event)} autoComplete="off">
      <FieldGroup>
        {stale && (
          <div role="alert" className="text-sm">
            <p>App changed. Reload settings before saving.</p>
            <p>Reloading discards unsaved edits.</p>
            <Button type="button" variant="outline" disabled={busy} onClick={onReload}>
              Reload settings
            </Button>
          </div>
        )}
        <fieldset disabled={fieldsDisabled} className="flex flex-col gap-6">
          <ProjectAppConnectionPicker
            app={app}
            provider={provider}
            providerLabel={providerLabels[provider]}
            lookup={connectionLookup}
            savedConnection={savedConnection}
            locked={savedConnection !== undefined || savedSecret !== ''}
            onConnectSlack={onConnectSlack}
          />
          {!settingsReady && (
            <p className="text-muted-foreground text-sm">
              Connect Slack and choose an active connection before configuring your app.
            </p>
          )}
          {settingsReady && (
            <>
              <Field>
                <FieldLabel htmlFor="app-name">App setup name</FieldLabel>
                <Input
                  id="app-name"
                  name="name"
                  required
                  maxLength={64}
                  value={values.name}
                  onChange={(event) => {
                    change({ name: event.target.value })
                  }}
                />
              </Field>
              {creating && (
                <NewAppConnectionFields
                  provider={provider}
                  providerLabel={providerLabels[provider]}
                  savedSecret={savedSecret}
                  newCredential={newCredential}
                  secretsQuery={secretsQuery}
                  onDifferentCredentials={() => {
                    setSavedSecret('')
                    setNewCredential(false)
                  }}
                  onNewCredentialChange={setNewCredential}
                  keyRequired={keyStatus.required}
                  keyPattern={keyStatus.pattern}
                />
              )}
              <ProjectAppCapabilityFields
                provider={provider}
                values={values}
                onChange={change}
                app={app}
              />
              <ProjectAppLauncherFields
                orgId={orgId}
                projectId={projectId}
                provider={provider}
                app={app}
                values={values}
                onChange={change}
                disabled={fieldsDisabled}
                workspaceId={connection?.provider_tenant_id}
                slotCount={slotCount}
              />
              {keyStatus.required && <DiscordAppInteractionsSetup connection={connection} />}
            </>
          )}
        </fieldset>
        <ProjectAppFormFooter
          busy={busy}
          blocked={cannotSubmit}
          editing={Boolean(app)}
          savedConnection={savedConnection}
          error={error}
          validationError={settingsReady ? validationError : ''}
          missingSavedKey={missingSavedKey}
          onCancel={onCancel}
        />
      </FieldGroup>
    </form>
  )
}
