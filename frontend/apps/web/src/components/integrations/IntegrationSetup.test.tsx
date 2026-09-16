/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import {
  createOmnaraClient,
  type IntegrationApp,
  type IntegrationInstall,
  type Secret,
  type VisibleProject,
} from '@omnara/sdk'
import {
  listEligibleIntegrationAppsQueryKey,
  listIntegrationAppsQueryKey,
} from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { CreateSecretDialog } from '@/components/org/CreateSecretDialog'
import { CredentialSecretField } from '@/components/secrets/CredentialSecretField'
import { fakeApi, type FakeRoute, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, field, submit, waitForUI } from '@/test/secret-editor'

import { CreateIntegrationAppDialog, EditIntegrationAppDialog } from './IntegrationAppDialogs'
import { IntegrationAppsSection } from './IntegrationAppsSection'
import { ProjectIntegrationsSection } from './ProjectIntegrationsSection'
import { RegisteredChannelsDialog } from './RegisteredChannelsDialog'

const orgId = fakeId('org')
const projectId = fakeId('proj')
const base = `/api/v1/orgs/${orgId}`
const now = '2026-09-15T00:00:00Z'
const project: VisibleProject = {
  id: projectId,
  org_id: orgId,
  name: 'restricted-project',
  created_at: now,
  updated_at: now,
  access: { can_read: true, can_manage: true, can_manage_access: true, can_operate: true },
}
const app: IntegrationApp = {
  id: fakeId('iapp'),
  org_id: orgId,
  provider: 'github',
  provider_app_ref: '123',
  name: 'team-github',
  state: 'active',
  created_at: now,
  updated_at: now,
  credential_secret_id: fakeId('sec'),
  provider_config: { client_id: 'client-123' },
}
const secret: Secret = {
  id: fakeId('sec'),
  org_id: orgId,
  name: 'app-credentials',
  kind: 'integration_credentials',
  management_kind: 'tenant',
  owner: { kind: 'org' },
  metadata: {},
  payload_keys: ['private_key', 'webhook_secret', 'client_secret'],
  current_version_number: 1,
  created_at: now,
  updated_at: now,
}
const install: IntegrationInstall = {
  id: fakeId('iin'),
  org_id: orgId,
  project_id: projectId,
  integration_app_id: app.id,
  provider: 'discord',
  integration_kind: 'managed',
  connection_mode: 'guild',
  state: 'active',
  display_name: 'Team server',
  metadata: {},
  created_at: now,
  updated_at: now,
}
let root: Root
let container: HTMLDivElement
let restore: () => void
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  restore()
})

async function render(ui: ReactNode, routes: FakeRoute[] = []) {
  const api = fakeApi([
    ...routes,
    {
      method: 'GET',
      path: base + '/projects',
      respond: () => jsonResponse({ data: [project], next_cursor: null }),
    },
    {
      method: 'GET',
      path: base + '/secrets',
      respond: () => jsonResponse({ data: [secret], next_cursor: null }),
    },
    { method: 'GET', path: base + '/secrets/' + secret.id, respond: () => jsonResponse(secret) },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await Promise.resolve()
  })
  return { api, queryClient }
}

async function choose(triggerId: string, label: string) {
  act(() => {
    const trigger = document.getElementById(triggerId)
    if (!trigger) throw new Error('Missing trigger ' + triggerId)
    trigger.click()
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="option"]')).not.toBeNull()
  })
  act(() => {
    const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(
      (item) => item.textContent.trim() === label,
    )
    if (!option) throw new Error('Missing option ' + label)
    option.click()
  })
}

it('does not request org app administration or secrets for a non-admin', async () => {
  const { api } = await render(<IntegrationAppsSection orgId={orgId} canManage={false} />)
  expect(document.body.textContent).toContain('Organization administrators manage apps')
  expect(api.requests).toHaveLength(0)
  expect(document.body.textContent).not.toContain('New app')
})

it('lists project summaries and connections without admin requests or manager controls', async () => {
  const { api } = await render(
    <ProjectIntegrationsSection orgId={orgId} projectId={projectId} canManage={false} />,
    [
      {
        method: 'GET',
        path: `${base}/projects/${projectId}/integration-apps`,
        respond: () => jsonResponse({ data: [app], next_cursor: null }),
      },
      {
        method: 'GET',
        path: `${base}/projects/${projectId}/integration-installs`,
        respond: () => jsonResponse({ data: [{ ...install, metadata: {} }], next_cursor: null }),
      },
    ],
  )
  await waitForUI(() => {
    expect(document.body.textContent).toContain('team-github')
    expect(document.body.textContent).toContain('Team server')
  })
  expect(document.body.textContent).not.toContain('Disconnect')
  expect(document.body.textContent).not.toContain('client-123')
  expect(api.requests).toHaveLength(2)
  expect(
    api.requests.every((request) =>
      request.url.pathname.startsWith(`${base}/projects/${projectId}/`),
    ),
  ).toBe(true)
})

it('creates a shared GitHub app with public config and only a credential secret ID', async () => {
  const close = vi.fn()
  const { api } = await render(<CreateIntegrationAppDialog orgId={orgId} onClose={close} />, [
    { method: 'POST', path: base + '/integration-apps', respond: () => jsonResponse(app, 201) },
  ])
  await enter('Name', 'team-github')
  await enter('App ID', '123')
  await enter('Client ID', 'client-123')
  await waitForUI(() => {
    expect(button('Search secrets…')).toBeDefined()
  })
  act(() => {
    button('Search secrets…').click()
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="option"]')?.textContent).toBe('app-credentials')
  })
  act(() => {
    document.querySelector<HTMLElement>('[role="option"]')?.click()
  })
  await submit()
  expect(api.requestsTo('POST', base + '/integration-apps').map((request) => request.body)).toEqual(
    [
      {
        provider: 'github',
        provider_app_ref: '123',
        name: 'team-github',
        credential_secret_id: secret.id,
        provider_config: { client_id: 'client-123' },
      },
    ],
  )
  expect(close).toHaveBeenCalled()
})

it('creates restricted Discord credentials under the exact project and sends empty public config', async () => {
  const restrictedSecret = {
    ...secret,
    owner: { kind: 'project' as const, project_id: projectId },
    payload_keys: ['bot_token', 'client_secret'],
  }
  const { api } = await render(<CreateIntegrationAppDialog orgId={orgId} onClose={vi.fn()} />, [
    { method: 'POST', path: base + '/secrets', respond: () => jsonResponse(restrictedSecret, 201) },
    {
      method: 'POST',
      path: base + '/integration-apps',
      respond: () =>
        jsonResponse(
          { ...app, provider: 'discord', owner_project_id: projectId, provider_config: {} },
          201,
        ),
    },
  ])
  await enter('Name', 'team-discord')
  await choose('app-provider', 'Discord')
  await enter('Application ID', '456')
  await choose('app-project', 'restricted-project')
  act(() => {
    button('Search secrets…').click()
  })
  await waitForUI(() => {
    expect(button('New secret')).toBeDefined()
  })
  act(() => {
    button('New secret').click()
  })
  await enter('Bot token', 'synthetic-bot-token')
  await enter('Client secret', 'synthetic-client-secret')
  act(() => {
    button('Create secret').click()
  })
  await waitForUI(() => {
    expect(api.requestsTo('POST', base + '/secrets')).toHaveLength(1)
    expect(document.body.textContent).not.toContain('Secret name')
  })
  await submit()
  expect(api.requestsTo('POST', base + '/secrets').map((request) => request.body)).toEqual([
    {
      owner: { kind: 'project', project_id: projectId },
      name: 'team-discord-credentials',
      material: {
        kind: 'integration_credentials',
        values: { bot_token: 'synthetic-bot-token', client_secret: 'synthetic-client-secret' },
      },
    },
  ])
  expect(api.requestsTo('POST', base + '/integration-apps').map((request) => request.body)).toEqual(
    [
      {
        provider: 'discord',
        provider_app_ref: '456',
        name: 'team-discord',
        owner_project_id: projectId,
        credential_secret_id: secret.id,
        provider_config: {},
      },
    ],
  )
  expect(api.requests.every((request) => !request.url.href.includes('synthetic'))).toBe(true)
})

it('filters out org and wrong-project secrets for a restricted app even for a resolved selection', async () => {
  const { api } = await render(
    <CredentialSecretField
      orgId={orgId}
      enabled
      value={secret.id}
      onChange={vi.fn()}
      label="Credentials"
      emptyDescription="No matching credentials"
      kind="integration_credentials"
      integrationProvider="github"
      owner={{ kind: 'project', project_id: projectId }}
    />,
  )
  await waitForUI(() => {
    expect(api.requestsTo('GET', base + '/secrets/' + secret.id)).toHaveLength(1)
  })
  expect(document.body.textContent).not.toContain('app-credentials')
  act(() => {
    button('Search secrets…').click()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain('No matching secrets found')
  })
  expect(
    api.requestsTo('GET', base + '/secrets')[0]?.url.searchParams.get('owner_project_id'),
  ).toBe(projectId)
  expect(api.requests.some((request) => request.url.pathname.includes('available-secrets'))).toBe(
    false,
  )
})

it('edits only mutable app fields and retains a failed draft for retry', async () => {
  let attempts = 0
  const close = vi.fn()
  const { api } = await render(
    <EditIntegrationAppDialog orgId={orgId} appId={app.id} onClose={close} />,
    [
      {
        method: 'GET',
        path: base + '/integration-apps/' + app.id,
        respond: () => jsonResponse(app),
      },
      {
        method: 'PATCH',
        path: base + '/integration-apps/' + app.id,
        respond: () =>
          ++attempts === 1
            ? jsonResponse({ code: 'conflict', message: 'App already exists' }, 409)
            : jsonResponse(app),
      },
    ],
  )
  await waitForUI(() => {
    expect(field('Name').value).toBe('team-github')
  })
  expect(field('App ID').readOnly).toBe(true)
  await enter('Name', 'renamed-app')
  await submit()
  expect(close).not.toHaveBeenCalled()
  expect(field('Name').value).toBe('renamed-app')
  await submit()
  expect(close).toHaveBeenCalled()
  expect(
    api.requestsTo('PATCH', base + '/integration-apps/' + app.id).map((request) => request.body),
  ).toEqual(
    Array.from({ length: 2 }, () => ({
      name: 'renamed-app',
    })),
  )
})

it.each([false, true])(
  'registers channels only for managers (canManage=%s) without creating agents',
  async (canManage) => {
    const path = `${base}/projects/${projectId}/integration-installs/${install.id}/channels`
    const { api } = await render(
      <RegisteredChannelsDialog
        orgId={orgId}
        projectId={projectId}
        install={install}
        canManage={canManage}
        onClose={vi.fn()}
      />,
      [
        { method: 'GET', path, respond: () => jsonResponse({ channels: [], next_cursor: null }) },
        {
          method: 'POST',
          path,
          respond: () =>
            jsonResponse({
              channel_id: fakeId('itgt'),
              definition_id: fakeId('cdef'),
              name: 'general',
              provider_ref: '12345',
              provider_ref_kind: 'channel',
            }),
        },
      ],
    )
    await waitForUI(() => {
      expect(document.body.textContent).toContain('No channels added yet')
    })
    if (canManage) {
      await enter('Channel ID', '12345')
      await submit()
      expect(api.requestsTo('POST', path).map((request) => request.body)).toEqual([
        { source: 'managed', provider_ref: '12345' },
      ])
    } else {
      expect(document.querySelector('form')).toBeNull()
    }
    expect(api.requests.every((request) => request.url.pathname === path)).toBe(true)
  },
)

it('creates Slack app credentials through the ordinary secret form', async () => {
  const close = vi.fn()
  const { api } = await render(
    <CreateSecretDialog open onOpenChange={close} orgId={orgId} owner={{ kind: 'org' }} />,
    [
      {
        method: 'POST',
        path: base + '/secrets',
        respond: () =>
          jsonResponse({ ...secret, payload_keys: ['signing_secret', 'client_secret'] }, 201),
      },
    ],
  )
  await enter('Name', 'slack-credentials')
  await choose('secret-kind', 'App credentials')
  await choose('credential-provider', 'Slack')
  await enter('Signing secret', 'synthetic-signing-secret')
  expect(button('Create secret').disabled).toBe(true)
  await enter('Client secret', 'synthetic-client-secret')
  act(() => {
    button('Create secret').click()
  })
  await waitForUI(() => {
    expect(close).toHaveBeenCalledWith(false)
  })
  expect(api.requestsTo('POST', base + '/secrets').map((request) => request.body)).toEqual([
    {
      owner: { kind: 'org' },
      name: 'slack-credentials',
      material: {
        kind: 'integration_credentials',
        values: {
          signing_secret: 'synthetic-signing-secret',
          client_secret: 'synthetic-client-secret',
        },
      },
    },
  ])
})

it('locks app ownership and dismissal while inline credentials are being created', async () => {
  let release: () => void = () => undefined
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  const close = vi.fn()
  const { api } = await render(<CreateIntegrationAppDialog orgId={orgId} onClose={close} />, [
    {
      method: 'POST',
      path: base + '/secrets',
      respond: async () => {
        await gate
        return jsonResponse(secret, 201)
      },
    },
  ])
  await enter('Name', 'team-github')
  act(() => {
    button('Search secrets…').click()
  })
  await waitForUI(() => {
    expect(button('New secret')).toBeDefined()
  })
  act(() => {
    button('New secret').click()
  })
  expect(document.getElementById('app-provider')?.hasAttribute('disabled')).toBe(true)
  expect(document.getElementById('app-project')?.hasAttribute('disabled')).toBe(true)

  await enter('Private key', 'synthetic-private-key')
  await enter('Webhook secret', 'synthetic-webhook-secret')
  await enter('Client secret', 'synthetic-client-secret')
  act(() => {
    button('Create secret').click()
  })
  await waitForUI(() => {
    expect(api.requestsTo('POST', base + '/secrets')).toHaveLength(1)
  })
  expect(document.querySelector('form > fieldset')?.hasAttribute('disabled')).toBe(true)
  expect(document.getElementById('app-project')?.hasAttribute('disabled')).toBe(true)
  act(() => {
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
  })
  expect(close).not.toHaveBeenCalled()
  await act(async () => {
    release()
    await gate
  })
  await waitForUI(() => {
    expect(document.querySelector('form > fieldset')?.hasAttribute('disabled')).toBe(false)
  })
  expect(api.requestsTo('POST', base + '/integration-apps')).toHaveLength(0)
})

it('does not save an old credential selection while drafting a replacement secret', async () => {
  await render(<EditIntegrationAppDialog orgId={orgId} appId={app.id} onClose={vi.fn()} />, [
    { method: 'GET', path: base + '/integration-apps/' + app.id, respond: () => jsonResponse(app) },
  ])
  await waitForUI(() => {
    expect(button('Save changes').disabled).toBe(false)
  })
  act(() => {
    button('Search secrets…').click()
  })
  await waitForUI(() => {
    expect(button('New secret')).toBeDefined()
  })
  act(() => {
    button('New secret').click()
  })
  expect(button('Save changes').disabled).toBe(true)
  act(() => {
    button('Cancel').click()
  })
  expect(button('Save changes').disabled).toBe(false)
})

it('refreshes org and project app lists after app changes without touching another org', async () => {
  const { queryClient } = await render(
    <EditIntegrationAppDialog orgId={orgId} appId={app.id} onClose={vi.fn()} />,
    [
      {
        method: 'GET',
        path: base + '/integration-apps/' + app.id,
        respond: () => jsonResponse(app),
      },
      {
        method: 'PATCH',
        path: base + '/integration-apps/' + app.id,
        respond: () => jsonResponse({ ...app, state: 'disabled' }),
      },
    ],
  )
  const orgKey = listIntegrationAppsQueryKey({ path: { orgID: orgId } })
  const projectKey = listEligibleIntegrationAppsQueryKey({
    path: { orgID: orgId, projectID: projectId },
  })
  const otherKey = listIntegrationAppsQueryKey({ path: { orgID: 'other-org' } })
  for (const key of [orgKey, projectKey, otherKey])
    queryClient.setQueryData(key, { data: [], next_cursor: null })
  await waitForUI(() => {
    expect(field('Name').value).toBe(app.name)
  })
  await choose('app-state', 'Disabled')
  await submit()
  expect(queryClient.getQueryState(orgKey)?.isInvalidated).toBe(true)
  expect(queryClient.getQueryState(projectKey)?.isInvalidated).toBe(true)
  expect(queryClient.getQueryState(otherKey)?.isInvalidated).toBe(false)
})

it('can disable a legacy Slack app without changing absent credentials or its old name', async () => {
  const { credential_secret_id: _credential, ...legacyApp } = app
  const legacy = { ...legacyApp, provider: 'slack', name: '', provider_config: {} }
  const { api } = await render(
    <EditIntegrationAppDialog orgId={orgId} appId={app.id} onClose={vi.fn()} />,
    [
      {
        method: 'GET',
        path: base + '/integration-apps/' + app.id,
        respond: () => jsonResponse(legacy),
      },
      {
        method: 'PATCH',
        path: base + '/integration-apps/' + app.id,
        respond: () => jsonResponse({ ...legacy, state: 'disabled' }),
      },
    ],
  )
  await waitForUI(() => {
    expect(button('Save changes').disabled).toBe(false)
  })
  await choose('app-state', 'Disabled')
  await submit()
  expect(
    api.requestsTo('PATCH', base + '/integration-apps/' + app.id).map((request) => request.body),
  ).toEqual([{ state: 'disabled' }])
})

it.each([false, true])(
  'registers GitHub pull requests only for managers (canManage=%s)',
  async (canManage) => {
    const path = `${base}/projects/${projectId}/integration-installs/${install.id}/channels`
    const repositoryId = '9007199254740993'
    const providerRef = `repo:${repositoryId}:pr:42`
    const { api } = await render(
      <RegisteredChannelsDialog
        orgId={orgId}
        projectId={projectId}
        install={{ ...install, provider: 'github', provider_account_ref: repositoryId }}
        canManage={canManage}
        onClose={vi.fn()}
      />,
      [
        { method: 'GET', path, respond: () => jsonResponse({ channels: [], next_cursor: null }) },
        {
          method: 'POST',
          path,
          respond: () =>
            jsonResponse({
              channel_id: fakeId('itgt'),
              definition_id: fakeId('cdef'),
              name: 'team/repo#42',
              provider_ref: providerRef,
              provider_ref_kind: 'pr',
            }),
        },
      ],
    )
    await waitForUI(() => {
      expect(document.body.textContent).toContain('No channels added yet')
    })
    if (canManage) {
      await enter('Pull request number', '42')
      await submit()
      expect(api.requestsTo('POST', path).map((request) => request.body)).toEqual([
        { source: 'managed', provider_ref: providerRef, provider_ref_kind: 'pr' },
      ])
      expect(document.body.textContent).toContain('Added team/repo#42')
      await enter('Pull request number', '0')
      await submit()
      expect(document.body.textContent).toContain('Enter a valid pull request number')
      expect(api.requestsTo('POST', path)).toHaveLength(1)
    } else {
      expect(document.querySelector('form')).toBeNull()
      expect(api.requestsTo('POST', path)).toHaveLength(0)
    }
    expect(api.requests.every((request) => request.url.pathname === path)).toBe(true)
  },
)
