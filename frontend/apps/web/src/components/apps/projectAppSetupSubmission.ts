import {
  type CreateSecretRequest,
  type IntegrationConnection,
  type ProfileAppProvider,
  type ProjectApp,
  type SaveIntegrationConnectionRequest,
  type SaveProjectAppRequest,
  type Secret,
} from '@omnara/sdk'
import * as z from 'zod'

import {
  projectAppFormDiscordKeyStatus,
  projectAppFormRequest,
  type ProjectAppFormValues,
} from './projectAppFormState'

/** Save each completed step before proceeding so retries reuse its secret and connection. */
export async function submitProjectAppSetup(
  input: {
    form: FormData
    projectId: string
    provider: ProfileAppProvider
    values: ProjectAppFormValues
    app?: ProjectApp
    connection?: IntegrationConnection
    creating: boolean
    savedSecret: string
    newCredential: boolean
  },
  actions: {
    createSecret: (body: CreateSecretRequest) => Promise<Secret>
    createConnection: (body: SaveIntegrationConnectionRequest) => Promise<IntegrationConnection>
    createApp: (body: SaveProjectAppRequest) => Promise<ProjectApp>
    updateApp: (body: SaveProjectAppRequest & { appID: string }) => Promise<ProjectApp>
    onSecretSaved: (id: string) => void
    onConnectionSaved: (connection: IntegrationConnection) => void
  },
) {
  const { form, provider, values, app } = input
  const value = (key: string) =>
    z
      .string()
      .parse(form.get(key) ?? '')
      .trim()
  if (app && input.creating) throw new Error('The configured connection cannot be changed here.')
  let selected = input.connection
  const request = projectAppFormRequest(
    provider,
    values,
    app?.settings.resource.connection ?? selected?.id,
    app,
    selected?.provider_tenant_id,
  )
  const providerConfig: Record<string, string | number> = {}
  if (input.creating && provider === 'discord') {
    providerConfig.shard_count = z.coerce.number().int().min(1).max(4096).parse(value('shards'))
    if (value('publicKey')) providerConfig.public_key = value('publicKey')
  }
  if (
    projectAppFormDiscordKeyStatus(
      request,
      app,
      input.creating ? providerConfig : selected?.provider_config,
    ).missing
  ) {
    throw new Error(
      'Save a valid public_key on the Discord connection to enable multiple choices or agent questions.',
    )
  }
  if (!app && !input.creating && (selected?.state !== 'active' || selected.provider !== provider))
    throw new Error('Choose an active connection for this provider.')
  if (input.creating) {
    let secretId = input.savedSecret || value('secret')
    if (!secretId && input.newCredential) {
      const secret = await actions.createSecret({
        owner: { kind: 'project', project_id: input.projectId },
        name: value('secretName'),
        material:
          provider === 'github'
            ? {
                kind: 'github_app_credentials',
                app_id: value('tenant'),
                private_key: value('privateKey'),
                webhook_secret: value('webhookSecret'),
              }
            : { kind: 'generic', value: value('botToken') },
      })
      secretId = secret.id
      actions.onSecretSaved(secret.id)
    }
    selected = await actions.createConnection({
      provider,
      provider_tenant_id: value('tenant'),
      provider_account_ref: value('account'),
      provider_agent_display_name: values.name.trim(),
      credential_secret_id: secretId,
      provider_config: providerConfig,
    })
    actions.onConnectionSaved(selected)
  }
  if (!app && (selected?.state !== 'active' || selected.provider !== provider))
    throw new Error('Choose an active connection for this provider.')
  if (!app && selected) request.settings.resource.connection = selected.id
  return app ? actions.updateApp({ appID: app.id, ...request }) : actions.createApp(request)
}
