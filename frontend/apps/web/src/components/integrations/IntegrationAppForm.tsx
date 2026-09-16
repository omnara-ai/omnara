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
  const [name, setName] = useState(app?.name ?? '')
  const [provider, setProvider] = useState<IntegrationAppProvider>(
    integrationProviders.find((item) => item.value === app?.provider)?.value ?? 'github',
  )
  const [appRef, setAppRef] = useState(app?.provider_app_ref ?? '')
  const [clientId, setClientId] = useState(app?.provider_config.client_id ?? '')
  const [project, setProject] = useState<VisibleProject | null>(null)
  const [secretId, setSecretId] = useState(app?.credential_secret_id ?? '')
  const [state, setState] = useState<IntegrationAppState>(app?.state ?? 'active')
  const [error, setError] = useState('')
  const [draftingCredentials, setDraftingCredentials] = useState(false)
  const projectsQuery = useVisibleProjectsList(orgId, { enabled: !app })
  const projects = useInfiniteQueryItems(projectsQuery)
  const ownerProjectId = app ? app.owner_project_id : project?.id
  const valid =
    !draftingCredentials &&
    (name === app?.name || resourceNameValid(name)) &&
    appRef.trim() !== '' &&
    (secretId !== '' || (app !== undefined && !app.credential_secret_id)) &&
    (provider === 'discord' ||
      clientId.trim() !== '' ||
      (app !== undefined && !app.provider_config.client_id && clientId === ''))

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
          <Field>
            <FieldLabel htmlFor="app-name">Name</FieldLabel>
            <Input
              id="app-name"
              required={!app || name !== app.name}
              value={name}
              placeholder="team-github"
              onChange={(event) => {
                setName(event.target.value)
              }}
            />
            {name !== app?.name && <ResourceNameFieldError value={name} />}
          </Field>
          <Field>
            <FieldLabel htmlFor="app-provider">Provider</FieldLabel>
            <Select
              value={provider}
              disabled={Boolean(app) || pending || draftingCredentials}
              onValueChange={(next) => {
                const option = integrationProviders.find((item) => item.value === next)
                if (!option) return
                setProvider(option.value)
                setAppRef('')
                setClientId('')
                setSecretId('')
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
                setAppRef(event.target.value)
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
                  setClientId(event.target.value)
                }}
              />
              {clientId !== '' && !clientId.trim() && (
                <FieldDescription>Enter a client ID.</FieldDescription>
              )}
            </Field>
          )}
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
                disabled={pending || draftingCredentials}
                placeholder="All projects in this organization"
                onValueChange={(next) => {
                  setProject(next)
                  setSecretId('')
                }}
              />
            )}
            <FieldDescription>
              Choose one project to restrict this app. Availability is fixed when the app is
              created.
            </FieldDescription>
          </Field>
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
            onChange={setSecretId}
            onPendingChange={onCredentialsPendingChange}
            onCreatingChange={setDraftingCredentials}
            label="Credentials"
            defaultSecretName={name ? `${name}-credentials` : ''}
            emptyDescription="Create app credentials or select an existing secret with all required fields."
          />
          {app && (
            <Field>
              <FieldLabel htmlFor="app-state">Status</FieldLabel>
              <Select
                value={state}
                disabled={pending}
                onValueChange={(next) => {
                  if (next === 'active' || next === 'disabled') setState(next)
                }}
              >
                <SelectTrigger id="app-state">
                  <SelectValue>{state === 'active' ? 'Active' : 'Disabled'}</SelectValue>
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="active">Active</SelectItem>
                  <SelectItem value="disabled">Disabled</SelectItem>
                </SelectContent>
              </Select>
              {state === 'disabled' && (
                <FieldDescription>
                  Disabling this app stops its connections in all projects that use it.
                </FieldDescription>
              )}
            </Field>
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
