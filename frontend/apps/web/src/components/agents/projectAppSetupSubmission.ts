import {
  type CreateSecretRequest,
  type IntegrationConnection,
  profileAppDiscordKeyStatus,
  type ProfileAppProvider,
  profileAppSetup,
  type ProjectApp,
  type SaveIntegrationConnectionRequest,
  type SaveProjectAppRequest,
  type Secret,
} from '@omnara/sdk'
import * as z from 'zod'

/** Save each completed step before proceeding so retries reuse its secret and connection. */
export async function submitProjectAppSetup(
  input: {
    form: FormData
    projectId: string
    provider: ProfileAppProvider
    profileId: string
    profileIds: string[]
    interactions: boolean
    connection?: IntegrationConnection
    creating: boolean
    savedSecret: string
    newCredential: boolean
  },
  actions: {
    createSecret: (body: CreateSecretRequest) => Promise<Secret>
    createConnection: (body: SaveIntegrationConnectionRequest) => Promise<IntegrationConnection>
    createApp: (body: SaveProjectAppRequest) => Promise<ProjectApp>
    onSecretSaved: (id: string) => void
    onConnectionSaved: (connection: IntegrationConnection) => void
  },
) {
  const { form, provider, profileIds, interactions } = input
  const value = (key: string) =>
    z
      .string()
      .parse(form.get(key) ?? '')
      .trim()
  if (provider !== 'github' && (profileIds.length === 0 || profileIds.length > 16)) {
    throw new Error('Choose between 1 and 16 profiles.')
  }
  const keyConfig = input.creating
    ? { public_key: value('publicKey') }
    : input.connection?.provider_config
  if (
    profileAppDiscordKeyStatus({
      definition: `omnara.${provider}`,
      slots: profileIds.map((id) => ({ agent_profile_id: id })),
      interactions,
      providerConfig: keyConfig,
    }).missing
  ) {
    throw new Error(
      'Save a valid public_key on the Discord connection to enable multiple choices or agent questions.',
    )
  }
  let selected = input.connection
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
    const providerConfig: Record<string, string | number> = {}
    if (provider === 'discord') {
      providerConfig.shard_count = Number(value('shards'))
      if (value('publicKey')) providerConfig.public_key = value('publicKey')
    }
    selected = await actions.createConnection({
      provider,
      provider_tenant_id: value('tenant'),
      provider_account_ref: value('account'),
      provider_agent_display_name: value('name'),
      credential_secret_id: secretId,
      provider_config: providerConfig,
    })
    actions.onConnectionSaved(selected)
  }
  if (selected?.state !== 'active') throw new Error('Choose an active connection.')
  await actions.createApp(
    profileAppSetup({
      provider,
      name: value('name'),
      connectionId: selected.id,
      profileId: input.profileId,
      profileIds: provider === 'github' ? undefined : profileIds,
      scopeRef: provider === 'slack' ? selected.provider_tenant_id : value('scope'),
      tools: z.array(z.string()).parse(form.getAll('tools')),
      listen: form.has('listen'),
      interactions,
    }),
  )
}
