import type { IntegrationAppProvider, IntegrationCredentialsSecretMaterial } from '@omnara/sdk'

export const integrationProviders = [
  { value: 'github', label: 'GitHub' },
  { value: 'discord', label: 'Discord' },
  { value: 'slack', label: 'Slack' },
] satisfies { value: IntegrationAppProvider; label: string }[]

export function integrationProviderLabel(provider: string) {
  return integrationProviders.find((option) => option.value === provider)?.label ?? provider
}

export const integrationCredentialFields = [
  { value: 'private_key', label: 'Private key' },
  { value: 'webhook_secret', label: 'Webhook secret' },
  { value: 'bot_token', label: 'Bot token' },
  { value: 'signing_secret', label: 'Signing secret' },
  { value: 'client_secret', label: 'Client secret' },
]

const providerKeys: Record<IntegrationAppProvider, string[]> = {
  github: ['private_key', 'webhook_secret', 'client_secret'],
  discord: ['bot_token', 'client_secret'],
  slack: ['signing_secret', 'client_secret'],
}

export function integrationFields(provider: IntegrationAppProvider) {
  return integrationCredentialFields.filter((field) => providerKeys[provider].includes(field.value))
}

export interface IntegrationCredentialsDraft {
  kind: 'integration_credentials'
  provider: IntegrationAppProvider
  values: Record<string, string>
}

export function newIntegrationCredentials(
  provider: IntegrationAppProvider = 'github',
): IntegrationCredentialsDraft {
  return { kind: 'integration_credentials', provider, values: {} }
}

export function integrationCredentialsMaterial(
  draft: IntegrationCredentialsDraft,
): IntegrationCredentialsSecretMaterial | undefined {
  const fields = integrationFields(draft.provider)
  if (fields.some((field) => !draft.values[field.value]?.trim())) return undefined
  return {
    kind: 'integration_credentials',
    values: Object.fromEntries(
      fields.map((field) => [field.value, draft.values[field.value] ?? '']),
    ),
  }
}
