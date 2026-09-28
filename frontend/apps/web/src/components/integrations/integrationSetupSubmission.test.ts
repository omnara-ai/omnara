import { expect, it, vi } from 'vitest'

import { fakeId, integration as integrationFixture } from '@/test/fixtures'

import { submitIntegrationSetup } from './integrationSetupSubmission'

it.each([
  ['discord_thread', 'tenant'],
  ['github_pr', 'account'],
  ['discord_thread', 'discord-key'],
] as const)('rejects invalid %s %s before saving credentials', async (integrationKind, invalid) => {
  const form = new FormData()
  form.set('tenant', '111')
  form.set('account', '222')
  form.set('publicKey', 'ab'.repeat(32))
  form.set('secretName', 'discord-credentials')
  form.set('botToken', 'test-token')
  form.set('privateKey', 'test-private-key')
  form.set('webhookSecret', 'test-webhook-secret')
  form.set(invalid === 'discord-key' ? 'publicKey' : invalid, 'invalid')
  type Actions = Parameters<typeof submitIntegrationSetup>[1]
  const actions: Actions = {
    createSecret: vi.fn<Actions['createSecret']>(),
    configureIntegration: vi.fn<Actions['configureIntegration']>(),
    onSecretSaved: vi.fn(),
  }
  await expect(
    submitIntegrationSetup(
      {
        form,
        integration: integrationFixture({ integration_kind: integrationKind }),
        projectId: fakeId('proj'),
        savedSecret: '',
        newCredential: true,
      },
      actions,
    ),
  ).rejects.toThrow()
  expect(actions.createSecret).not.toHaveBeenCalled()
  expect(actions.configureIntegration).not.toHaveBeenCalled()
})

it('pins the integration ID, revision and original provider identity on reconnect', async () => {
  const integration = integrationFixture({
    integration_kind: 'github_pr',
    provider_tenant_id: '111',
    provider_account_ref: '222',
    setup_revision: 8,
  })
  const form = new FormData()
  form.set('tenant', '999')
  form.set('account', '999')
  form.set('secret', fakeId('sec'))
  const configureIntegration = vi.fn(() => Promise.resolve(integration))
  await submitIntegrationSetup(
    { form, integration, projectId: integration.project_id, savedSecret: '', newCredential: false },
    { createSecret: vi.fn(), configureIntegration, onSecretSaved: vi.fn() },
  )
  expect(configureIntegration).toHaveBeenCalledWith({
    integrationID: integration.id,
    expected_setup_revision: 8,
    provider_tenant_id: '111',
    provider_account_ref: '222',
    credential_secret_id: fakeId('sec'),
    provider_config: {},
  })
})
