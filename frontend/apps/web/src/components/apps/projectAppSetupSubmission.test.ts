import { expect, it, vi } from 'vitest'

import { fakeId, projectApp } from '@/test/fixtures'

import { submitProjectAppSetup } from './projectAppSetupSubmission'

it.each([
  ['discord_thread', 'tenant'],
  ['github_pr', 'account'],
  ['discord_thread', 'discord-key'],
] as const)('rejects invalid %s %s before saving credentials', async (appType, invalid) => {
  const form = new FormData()
  form.set('tenant', '111')
  form.set('account', '222')
  form.set('publicKey', 'ab'.repeat(32))
  form.set('secretName', 'discord-credentials')
  form.set('botToken', 'test-token')
  form.set('privateKey', 'test-private-key')
  form.set('webhookSecret', 'test-webhook-secret')
  form.set(invalid === 'discord-key' ? 'publicKey' : invalid, 'invalid')
  type Actions = Parameters<typeof submitProjectAppSetup>[1]
  const actions: Actions = {
    createSecret: vi.fn<Actions['createSecret']>(),
    configureApp: vi.fn<Actions['configureApp']>(),
    onSecretSaved: vi.fn(),
  }
  await expect(
    submitProjectAppSetup(
      {
        form,
        app: projectApp({ app_type: appType }),
        projectId: fakeId('proj'),
        savedSecret: '',
        newCredential: true,
      },
      actions,
    ),
  ).rejects.toThrow()
  expect(actions.createSecret).not.toHaveBeenCalled()
  expect(actions.configureApp).not.toHaveBeenCalled()
})

it('pins the app ID, revision and original provider identity on reconnect', async () => {
  const app = projectApp({
    app_type: 'github_pr',
    provider_tenant_id: '111',
    provider_account_ref: '222',
    setup_revision: 8,
  })
  const form = new FormData()
  form.set('tenant', '999')
  form.set('account', '999')
  form.set('secret', fakeId('sec'))
  const configureApp = vi.fn(() => Promise.resolve(app))
  await submitProjectAppSetup(
    { form, app, projectId: app.project_id, savedSecret: '', newCredential: false },
    { createSecret: vi.fn(), configureApp, onSecretSaved: vi.fn() },
  )
  expect(configureApp).toHaveBeenCalledWith({
    appID: app.id,
    expected_setup_revision: 8,
    provider_tenant_id: '111',
    provider_account_ref: '222',
    provider_agent_display_name: '',
    credential_secret_id: fakeId('sec'),
    provider_config: {},
  })
})
