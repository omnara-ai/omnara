import { useVisibleProjectsList } from '@omnara/react'
import type {
  CreateIntegrationAppRequest,
  IntegrationApp,
  IntegrationAppProvider,
  IntegrationAppState,
  VisibleProject,
} from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import {
  integrationProviderLabel,
  integrationProviders,
} from '@/components/org/integrationCredentials'
import { CredentialSecretField } from '@/components/secrets/CredentialSecretField'
import { Button } from '@/components/ui/button'
import { DialogFooter } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { normalizeResourceName, resourceNameValid } from '@/lib/resource-name'
import { errorMessage } from '@/lib/submit-status'

const ProjectCombobox = createResourceCombobox<VisibleProject>({
  itemKey: (project) => project.id,
  itemLabel: (project) => project.name,
  placeholder: 'Search projects…',
})

export type IntegrationAppFormInput = CreateIntegrationAppRequest & { state: IntegrationAppState }

interface IntegrationAppDraft {
  name: string
  provider: IntegrationAppProvider
  appRef: string
  clientId: string
  secretId: string
  state: IntegrationAppState
}

function initialIntegrationAppDraft(app?: IntegrationApp): IntegrationAppDraft {
  return {
    name: app?.name ?? '',
    provider: integrationProviders.find((item) => item.value === app?.provider)?.value ?? 'github',
    appRef: app?.provider_app_ref ?? '',
    clientId: app?.provider_config.client_id ?? '',
    secretId: app?.credential_secret_id ?? '',
    state: app?.state ?? 'active',
  }
}

export function IntegrationAppForm({
  orgId,
  app,
  onSave,
  onCancel,
  pending,
  onCredentialsPendingChange,
}: {
  orgId: string
  app?: IntegrationApp
  onSave: (input: IntegrationAppFormInput) => Promise<void>
  onCancel: () => void
  pending: boolean
  onCredentialsPendingChange: (pending: boolean) => void
}) {
  const [draft, setDraft] = useState(() => initialIntegrationAppDraft(app))
  const { name, provider, appRef, clientId, secretId, state } = draft
  const [project, setProject] = useState<VisibleProject | null>(null)
  const [error, setError] = useState('')
  const [draftingCredentials, setDraftingCredentials] = useState(false)
  const ownerProjectId = app ? app.owner_project_id : project?.id
  const valid = !draftingCredentials && integrationAppFieldsValid(draft, app)

  function patchDraft(patch: Partial<IntegrationAppDraft>) {
    setDraft((current) => ({ ...current, ...patch }))
  }

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!valid || pending) return
    setError('')
    try {
      const input: IntegrationAppFormInput = {
        provider,
        provider_app_ref: appRef.trim(),
        name: name === app?.name ? name : normalizeResourceName(name),
        credential_secret_id: secretId,
        provider_config:
          provider === 'discord' || clientId === '' ? {} : { client_id: clientId.trim() },
        state,
      }
      if (ownerProjectId) input.owner_project_id = ownerProjectId
      await onSave(input)
    } catch (err) {
      setError(errorMessage(err, 'Could not save app'))
    }
  }

  return (
    <form autoComplete="off" onSubmit={(event) => void submit(event)}>
      <fieldset disabled={pending}>
        <FieldGroup className="gap-4">
          <IntegrationAppIdentityFields
            app={app}
            draft={draft}
            onChange={patchDraft}
            providerDisabled={Boolean(app) || pending || draftingCredentials}
          />
          <IntegrationAppAvailabilityField
            orgId={orgId}
            app={app}
            project={project}
            disabled={pending || draftingCredentials}
            onChange={(next) => {
              setProject(next)
              patchDraft({ secretId: '' })
            }}
          />
          <CredentialSecretField
            key={`${provider}:${ownerProjectId ?? 'org'}`}
            orgId={orgId}
            enabled={!pending}
            kind="integration_credentials"
            integrationProvider={provider}
            owner={
              ownerProjectId ? { kind: 'project', project_id: ownerProjectId } : { kind: 'org' }
            }
            value={secretId}
            onChange={(secretId) => {
              patchDraft({ secretId })
            }}
            onPendingChange={onCredentialsPendingChange}
            onCreatingChange={setDraftingCredentials}
            label="Credentials"
            defaultSecretName={name ? `${name}-credentials` : ''}
            emptyDescription="Create app credentials or select an existing secret with all required fields."
          />
          {app && (
            <IntegrationAppStatusField
              value={state}
              onChange={(state) => {
                patchDraft({ state })
              }}
              disabled={pending}
            />
          )}
          {error && (
            <p role="alert" className="text-destructive text-sm">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onCancel}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !valid} loading={pending}>
              {app ? 'Save changes' : 'Create app'}
            </Button>
          </DialogFooter>
        </FieldGroup>
      </fieldset>
    </form>
  )
}

function integrationAppFieldsValid(fields: IntegrationAppDraft, app?: IntegrationApp) {
  return (
    (fields.name === app?.name || resourceNameValid(fields.name)) &&
    fields.appRef.trim() !== '' &&
    (fields.secretId !== '' || (app !== undefined && !app.credential_secret_id)) &&
    (fields.provider === 'discord' ||
      fields.clientId.trim() !== '' ||
      (app !== undefined && !app.provider_config.client_id && fields.clientId === ''))
  )
}

function IntegrationAppAvailabilityField({
  orgId,
  app,
  project,
  disabled,
  onChange,
}: {
  orgId: string
  app?: IntegrationApp
  project: VisibleProject | null
  disabled: boolean
  onChange: (project: VisibleProject | null) => void
}) {
  const projectsQuery = useVisibleProjectsList(orgId, { enabled: !app })
  const projects = useInfiniteQueryItems(projectsQuery)
  return (
    <Field>
      <FieldLabel htmlFor="app-project">Available to</FieldLabel>
      {app ? (
        <p className="text-sm">
          {app.owner_project_id
            ? `Project ${app.owner_project_id}`
            : 'All projects in this organization'}
        </p>
      ) : (
        <ProjectCombobox
          id="app-project"
          items={projects}
          value={project}
          query={projectsQuery}
          disabled={disabled}
          placeholder="All projects in this organization"
          onValueChange={onChange}
        />
      )}
      <FieldDescription>
        Choose one project to restrict this app. Availability is fixed when the app is created.
      </FieldDescription>
    </Field>
  )
}

function IntegrationAppStatusField({
  value,
  onChange,
  disabled,
}: {
  value: IntegrationAppState
  onChange: (value: IntegrationAppState) => void
  disabled: boolean
}) {
  return (
    <Field>
      <FieldLabel htmlFor="app-state">Status</FieldLabel>
      <Select
        value={value}
        disabled={disabled}
        onValueChange={(next) => {
          if (next === 'active' || next === 'disabled') onChange(next)
        }}
      >
        <SelectTrigger id="app-state">
          <SelectValue>{value === 'active' ? 'Active' : 'Disabled'}</SelectValue>
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="active">Active</SelectItem>
          <SelectItem value="disabled">Disabled</SelectItem>
        </SelectContent>
      </Select>
      {value === 'disabled' && (
        <FieldDescription>
          Disabling this app stops its connections in all projects that use it.
        </FieldDescription>
      )}
    </Field>
  )
}

function IntegrationAppIdentityFields({
  app,
  draft,
  providerDisabled,
  onChange,
}: {
  app?: IntegrationApp
  draft: IntegrationAppDraft
  providerDisabled: boolean
  onChange: (patch: Partial<IntegrationAppDraft>) => void
}) {
  const { name, provider, appRef, clientId } = draft
  return (
    <>
      <Field>
        <FieldLabel htmlFor="app-name">Name</FieldLabel>
        <Input
          id="app-name"
          required={!app || name !== app.name}
          value={name}
          placeholder="team-github"
          onChange={(event) => {
            onChange({ name: event.target.value })
          }}
        />
        {name !== app?.name && <ResourceNameFieldError value={name} />}
      </Field>
      <Field>
        <FieldLabel htmlFor="app-provider">Provider</FieldLabel>
        <Select
          value={provider}
          disabled={providerDisabled}
          onValueChange={(next) => {
            const option = integrationProviders.find((item) => item.value === next)
            if (!option) return
            onChange({ provider: option.value, appRef: '', clientId: '', secretId: '' })
          }}
        >
          <SelectTrigger id="app-provider">
            <SelectValue>{integrationProviderLabel(provider)}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {integrationProviders.map((item) => (
              <SelectItem key={item.value} value={item.value}>
                {item.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <Field>
        <FieldLabel htmlFor="provider-app-id">
          {provider === 'discord' ? 'Application ID' : 'App ID'}
        </FieldLabel>
        <Input
          id="provider-app-id"
          required
          readOnly={Boolean(app)}
          maxLength={512}
          value={appRef}
          onChange={(event) => {
            onChange({ appRef: event.target.value })
          }}
        />
      </Field>
      {provider !== 'discord' && (
        <Field>
          <FieldLabel htmlFor="app-client-id">Client ID</FieldLabel>
          <Input
            id="app-client-id"
            required={!app || Boolean(app.provider_config.client_id)}
            maxLength={512}
            value={clientId}
            onChange={(event) => {
              onChange({ clientId: event.target.value })
            }}
          />
          {clientId !== '' && !clientId.trim() && (
            <FieldDescription>Enter a client ID.</FieldDescription>
          )}
        </Field>
      )}
    </>
  )
}
