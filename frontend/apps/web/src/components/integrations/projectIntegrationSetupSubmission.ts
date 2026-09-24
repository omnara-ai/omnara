import {
  type ConfigureProjectIntegrationRequest,
  type CreateSecretRequest,
  type ProjectIntegration,
  schemas,
  type Secret,
} from '@omnara/sdk'
import * as z from 'zod'

export async function submitProjectIntegrationSetup(
  input: {
    form: FormData
    projectId: string
    integration: ProjectIntegration
    savedSecret: string
    newCredential: boolean
  },
  actions: {
    createSecret: (body: CreateSecretRequest) => Promise<Secret>
    configureIntegration: (
      body: ConfigureProjectIntegrationRequest & { integrationID: string },
    ) => Promise<ProjectIntegration>
    onSecretSaved: (id: string) => void
  },
) {
  const { integration, form } = input
  const value = (key: string) =>
    z
      .string()
      .parse(form.get(key) ?? '')
      .trim()
  if (integration.integration_type === 'slack_thread')
    throw new Error('Use Slack authorization to connect this integration.')
  const tenant = integration.provider_tenant_id || value('tenant')
  const account =
    integration.integration_type === 'github_pr'
      ? integration.provider_account_ref || value('account')
      : undefined
  const identity = z.string().regex(/^[1-9][0-9]*$/, 'Enter a positive numeric provider ID.')
  identity.parse(tenant)
  if (integration.integration_type === 'github_pr') identity.parse(account)
  const providerConfig: ConfigureProjectIntegrationRequest['provider_config'] = {}
  if (integration.integration_type === 'discord_thread') {
    providerConfig.public_key = z
      .string()
      .regex(/^[a-fA-F0-9]{64}$/, 'Enter the 64-character Discord public key.')
      .parse(value('publicKey'))
  }
  let secretId = input.savedSecret || (input.newCredential ? '' : value('secret'))
  if (!secretId && input.newCredential) {
    const request: CreateSecretRequest = {
      owner: { kind: 'project', project_id: input.projectId },
      name: value('secretName'),
      material:
        integration.integration_type === 'github_pr'
          ? {
              kind: 'github_app_credentials',
              app_id: tenant,
              private_key: value('privateKey'),
              webhook_secret: value('webhookSecret'),
            }
          : { kind: 'generic', value: value('botToken') },
    }
    schemas.zCreateSecretRequest.parse(request)
    const secret = await actions.createSecret(request)
    secretId = secret.id
    actions.onSecretSaved(secret.id)
  }
  const body = schemas.zConfigureProjectIntegrationRequest.parse({
    expected_setup_revision: integration.setup_revision,
    provider_tenant_id: tenant,
    provider_account_ref: account,
    credential_secret_id: secretId,
    provider_config: providerConfig,
  })
  return actions.configureIntegration({ integrationID: integration.id, ...body })
}
