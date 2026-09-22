import {
  type ConfigureProjectAppRequest,
  type CreateSecretRequest,
  type ProjectApp,
  schemas,
  type Secret,
} from '@omnara/sdk'
import * as z from 'zod'

export async function submitProjectAppSetup(
  input: {
    form: FormData
    projectId: string
    app: ProjectApp
    savedSecret: string
    newCredential: boolean
  },
  actions: {
    createSecret: (body: CreateSecretRequest) => Promise<Secret>
    configureApp: (body: ConfigureProjectAppRequest & { appID: string }) => Promise<ProjectApp>
    onSecretSaved: (id: string) => void
  },
) {
  const { app, form } = input
  const value = (key: string) =>
    z
      .string()
      .parse(form.get(key) ?? '')
      .trim()
  if (app.app_type === 'slack_thread')
    throw new Error('Use Slack authorization to connect this app.')
  const tenant = app.provider_tenant_id || value('tenant')
  const account =
    app.app_type === 'github_pr' ? app.provider_account_ref || value('account') : undefined
  const identity = z.string().regex(/^[1-9][0-9]*$/, 'Enter a positive numeric provider ID.')
  identity.parse(tenant)
  if (app.app_type === 'github_pr') identity.parse(account)
  const providerConfig: ConfigureProjectAppRequest['provider_config'] = {}
  if (app.app_type === 'discord_thread') {
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
        app.app_type === 'github_pr'
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
  const body = schemas.zConfigureProjectAppRequest.parse({
    expected_setup_revision: app.setup_revision,
    provider_tenant_id: tenant,
    provider_account_ref: account,
    credential_secret_id: secretId,
    provider_config: providerConfig,
  })
  return actions.configureApp({ appID: app.id, ...body })
}
