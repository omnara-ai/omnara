/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type IntegrationAppSummary } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { fakeApi, type FakeRoute, jsonResponse } from '@/test/fake-api'
import { agentConfigModel, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, submit, waitForUI } from '@/test/secret-editor'

import { ConnectIntegrationAppDialog } from './ConnectIntegrationAppDialog'
import { GitHubConnectionDialog } from './GitHubConnectionDialog'
import { IntegrationConnectionOutcome } from './IntegrationConnectionOutcome'
import { integrationSetupSearch } from './integrationOAuth'

const orgId = fakeId('org')
const projectId = fakeId('proj')
const flowId = fakeId('ioaf')
const now = '2026-09-15T00:00:00Z'
const base = `/api/v1/orgs/${orgId}/projects/${projectId}`
const installationsPath = `${base}/integration-oauth/${flowId}/github/installations`
const completePath = `${base}/integration-oauth/${flowId}/github/complete`
const app: IntegrationAppSummary = {
  id: fakeId('iapp'),
  org_id: orgId,
  provider: 'github',
  provider_app_ref: '123',
  name: 'team-app',
  state: 'active',
  created_at: now,
  updated_at: now,
}
const connected = {
  id: fakeId('iin'),
  org_id: orgId,
  project_id: projectId,
  integration_app_id: app.id,
  provider: 'github',
  integration_kind: 'managed',
  connection_mode: 'repository',
  state: 'active',
  display_name: 'team/repo',
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
  vi.restoreAllMocks()
})

async function render(ui: ReactNode, routes: FakeRoute[] = []) {
  const api = fakeApi([
    ...routes,
    {
      method: 'GET',
      path: base + '/agent-profiles',
      respond: () => jsonResponse({ data: [], next_cursor: null }),
    },
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

it.each(['github', 'discord', 'slack'])(
  'starts %s setup without an agent or profile and redirects only to the returned authorization URL',
  async (provider) => {
    const assign = vi.spyOn(window.location, 'assign').mockImplementation(() => undefined)
    const { api } = await render(
      <ConnectIntegrationAppDialog
        orgId={orgId}
        projectId={projectId}
        app={{ ...app, provider }}
        onClose={vi.fn()}
      />,
      [
        {
          method: 'POST',
          path: base + '/integration-oauth/setup',
          respond: () =>
            jsonResponse(
              {
                provider,
                flow_id: flowId,
                oauth_url: 'https://authorize.example/consent?state=opaque',
                expires_at: '2026-09-15T01:00:00Z',
              },
              201,
            ),
        },
      ],
    )
    act(() => {
      document
        .querySelector('form')
        ?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })
    await waitForUI(() => {
      expect(assign).toHaveBeenCalledWith('https://authorize.example/consent?state=opaque')
    })
    expect(
      api.requestsTo('POST', base + '/integration-oauth/setup').map((request) => request.body),
    ).toEqual([{ integration_app_id: app.id, return_to: `/projects/${projectId}/integrations` }])
    expect(api.requests.some((request) => request.url.pathname.endsWith('/agents'))).toBe(false)
  },
)

it('includes a profile only when explicitly selected for inbound activity', async () => {
  const assign = vi.spyOn(window.location, 'assign').mockImplementation(() => undefined)
  const profile = {
    id: fakeId('aprf'),
    org_id: orgId,
    project_id: projectId,
    name: 'inbound-helper',
    current_config_id: fakeId('acfg'),
    current_generation: 1,
    current_config: {
      id: fakeId('acfg'),
      org_id: orgId,
      project_id: projectId,
      effective_definition_hash: 'hash',
      model: agentConfigModel(),
      created_at: now,
    },
    created_at: now,
    updated_at: now,
  }
  const { api } = await render(
    <ConnectIntegrationAppDialog orgId={orgId} projectId={projectId} app={app} onClose={vi.fn()} />,
    [
      {
        method: 'GET',
        path: base + '/agent-profiles',
        respond: () => jsonResponse({ data: [profile], next_cursor: null }),
      },
      {
        method: 'POST',
        path: base + '/integration-oauth/setup',
        respond: () =>
          jsonResponse(
            {
              provider: 'github',
              flow_id: flowId,
              oauth_url: 'https://github.com/login/oauth/authorize',
              expires_at: now,
            },
            201,
          ),
      },
    ],
  )
  act(() => {
    document.getElementById('inbound-profile')?.click()
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="option"]')?.textContent).toBe('inbound-helper')
  })
  act(() => {
    document.querySelector<HTMLElement>('[role="option"]')?.click()
  })
  act(() => {
    document
      .querySelector('form')
      ?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  })
  await waitForUI(() => {
    expect(assign).toHaveBeenCalled()
  })
  expect(
    api.requestsTo('POST', base + '/integration-oauth/setup').map((request) => request.body),
  ).toEqual([
    {
      integration_app_id: app.id,
      agent_profile_id: profile.id,
      return_to: `/projects/${projectId}/integrations`,
    },
  ])
})

it('keeps setup usable after an error and rejects unsafe redirect URLs', async () => {
  const assign = vi.spyOn(window.location, 'assign').mockImplementation(() => undefined)
  let attempts = 0
  const { api } = await render(
    <ConnectIntegrationAppDialog orgId={orgId} projectId={projectId} app={app} onClose={vi.fn()} />,
    [
      {
        method: 'POST',
        path: base + '/integration-oauth/setup',
        respond: () =>
          ++attempts === 1
            ? jsonResponse({ code: 'conflict', error: 'This account is already connected' }, 409)
            : jsonResponse(
                {
                  provider: 'github',
                  flow_id: flowId,
                  oauth_url: 'javascript:alert(1)',
                  expires_at: now,
                },
                201,
              ),
      },
    ],
  )
  await submit()
  expect(document.body.textContent).toContain('already connected')
  await submit()
  expect(document.body.textContent).toContain('Could not open the app authorization page')
  expect(api.requestsTo('POST', base + '/integration-oauth/setup')).toHaveLength(2)
  expect(assign).not.toHaveBeenCalled()
})

it('paginates verified installations and repositories, preserving large native IDs as strings', async () => {
  const installationId = '9007199254740993'
  const repositoryId = '9007199254740995'
  const done = vi.fn()
  const { api } = await render(
    <GitHubConnectionDialog
      orgId={orgId}
      projectId={projectId}
      flowId={flowId}
      onClose={vi.fn()}
      onConnected={done}
    />,
    [
      {
        method: 'GET',
        path: installationsPath,
        respond: (request) =>
          jsonResponse(
            request.url.searchParams.get('page') === '2'
              ? {
                  data: [{ id: installationId, account_login: 'team', suspended: false }],
                  installation_url: 'https://github.com/apps/team/installations/new',
                }
              : {
                  data: [{ id: '1', account_login: 'suspended-team', suspended: true }],
                  installation_url: 'https://github.com/apps/team/installations/new',
                  next_page: 2,
                },
          ),
      },
      {
        method: 'GET',
        path: `${installationsPath}/${installationId}/repositories`,
        respond: (request) =>
          jsonResponse(
            request.url.searchParams.get('page') === '2'
              ? {
                  data: [
                    { id: repositoryId, full_name: 'team/repo', private: true, can_connect: true },
                  ],
                }
              : {
                  data: [
                    { id: '2', full_name: 'team/read-only', private: false, can_connect: false },
                  ],
                  next_page: 2,
                },
          ),
      },
      { method: 'POST', path: completePath, respond: () => jsonResponse(connected, 201) },
    ],
  )
  await waitForUI(() => {
    expect(button('Select suspended-team').disabled).toBe(true)
  })
  const link = document.querySelector<HTMLAnchorElement>('a')
  expect(link?.href).toBe('https://github.com/apps/team/installations/new')
  expect(link?.target).toBe('_blank')
  expect(link?.rel).toContain('noopener')
  act(() => {
    button('Next').click()
  })
  await waitForUI(() => {
    expect(button('Select team').disabled).toBe(false)
  })
  act(() => {
    button('Select team').click()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Admin access required')
  })
  expect(
    [...document.querySelectorAll('button')].some((item) => item.textContent === 'Connect'),
  ).toBe(false)
  act(() => {
    button('Next').click()
  })
  await waitForUI(() => {
    expect(button('Connect team/repo').disabled).toBe(false)
  })
  act(() => {
    button('Connect team/repo').click()
  })
  await waitForUI(() => {
    expect(done).toHaveBeenCalled()
  })
  expect(api.requestsTo('POST', completePath).map((request) => request.body)).toEqual([
    {
      installation_id: installationId,
      repository_id: repositoryId,
      repository_full_name: 'team/repo',
    },
  ])
  expect(
    api.requestsTo('GET', installationsPath).map((request) => request.url.searchParams.get('page')),
  ).toEqual(['1', '2'])
})

it('refreshes installations after opening the verified installation page', async () => {
  let requests = 0
  await render(
    <GitHubConnectionDialog
      orgId={orgId}
      projectId={projectId}
      flowId={flowId}
      onClose={vi.fn()}
      onConnected={vi.fn()}
    />,
    [
      {
        method: 'GET',
        path: installationsPath,
        respond: () =>
          jsonResponse({
            data:
              ++requests === 1 ? [] : [{ id: '123', account_login: 'new-team', suspended: false }],
            installation_url: 'https://github.com/apps/team/installations/new',
          }),
      },
    ],
  )
  await waitForUI(() => {
    expect(document.body.textContent).toContain('No GitHub installations available')
  })
  act(() => {
    button('Refresh').click()
  })
  await waitForUI(() => {
    expect(button('Select new-team')).toBeDefined()
  })
})

it('shows a recoverable same-user/expired-flow failure', async () => {
  const { api } = await render(
    <GitHubConnectionDialog
      orgId={orgId}
      projectId={projectId}
      flowId={flowId}
      onClose={vi.fn()}
      onConnected={vi.fn()}
    />,
    [
      {
        method: 'GET',
        path: installationsPath,
        respond: () => jsonResponse({ code: 'forbidden', error: 'Not your flow' }, 403),
      },
    ],
  )
  await waitForUI(() => {
    expect(document.body.textContent).toContain('started by another user')
  })
  expect(document.querySelector('a')).toBeNull()
  expect(api.requestsTo('POST', completePath)).toHaveLength(0)
})

it.each([
  {
    canManage: false,
    search: { integration_oauth: 'select_repository', integration_oauth_flow_id: flowId },
    message: 'Project management access is required',
  },
  {
    canManage: true,
    search: { integration_oauth: 'select_repository', integration_oauth_flow_id: 'invalid' },
    message: 'setup link is incomplete',
  },
  {
    canManage: true,
    search: {
      integration_oauth: 'select_repository',
      integration_oauth_flow_id: flowId,
      integration_oauth_error: 'access_denied',
    },
    message: 'canceled or denied',
  },
])(
  'does not query GitHub for an unusable callback ($message)',
  async ({ canManage, search, message }) => {
    const close = vi.fn()
    const { api } = await render(
      <IntegrationConnectionOutcome
        orgId={orgId}
        projectId={projectId}
        canManage={canManage}
        search={integrationSetupSearch.parse(search)}
        onClose={close}
        onConnected={vi.fn()}
      />,
    )
    expect(document.body.textContent).toContain(message)
    expect(api.requests).toHaveLength(0)
    act(() => {
      button('Got it').click()
    })
    expect(close).toHaveBeenCalled()
  },
)

it('does not redirect from a delayed setup response after leaving the project', async () => {
  let release: () => void = () => undefined
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  const assign = vi.spyOn(window.location, 'assign').mockImplementation(() => undefined)
  const { api } = await render(
    <ConnectIntegrationAppDialog orgId={orgId} projectId={projectId} app={app} onClose={vi.fn()} />,
    [
      {
        method: 'POST',
        path: base + '/integration-oauth/setup',
        respond: async () => {
          await gate
          return jsonResponse(
            {
              provider: 'github',
              flow_id: flowId,
              oauth_url: 'https://github.com/login/oauth/authorize',
              expires_at: now,
            },
            201,
          )
        },
      },
    ],
  )
  act(() => {
    document
      .querySelector('form')
      ?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  })
  await waitForUI(() => {
    expect(api.requestsTo('POST', base + '/integration-oauth/setup')).toHaveLength(1)
  })
  act(() => {
    root.render(null)
  })
  await act(async () => {
    release()
    await gate
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  expect(assign).not.toHaveBeenCalled()
})

it('locks GitHub completion against dismissal and account changes until it settles', async () => {
  let release: () => void = () => undefined
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  const close = vi.fn()
  const done = vi.fn()
  const { api } = await render(
    <GitHubConnectionDialog
      orgId={orgId}
      projectId={projectId}
      flowId={flowId}
      onClose={close}
      onConnected={done}
    />,
    [
      {
        method: 'GET',
        path: installationsPath,
        respond: () =>
          jsonResponse({
            data: [{ id: '123', account_login: 'team', suspended: false }],
            installation_url: 'https://github.com/apps/team/installations/new',
          }),
      },
      {
        method: 'GET',
        path: installationsPath + '/123/repositories',
        respond: () =>
          jsonResponse({
            data: [{ id: '456', full_name: 'team/repo', private: true, can_connect: true }],
          }),
      },
      {
        method: 'POST',
        path: completePath,
        respond: async () => {
          await gate
          return jsonResponse(connected, 201)
        },
      },
    ],
  )
  await waitForUI(() => {
    expect(button('Select team').disabled).toBe(false)
  })
  act(() => {
    button('Select team').click()
  })
  await waitForUI(() => {
    expect(button('Connect team/repo').disabled).toBe(false)
  })
  act(() => {
    button('Connect team/repo').click()
  })
  await waitForUI(() => {
    expect(api.requestsTo('POST', completePath)).toHaveLength(1)
  })
  await waitForUI(() => {
    expect(button('Change account').disabled).toBe(true)
    expect(button('Connect team/repo').disabled).toBe(true)
  })
  act(() => {
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
  })
  expect(close).not.toHaveBeenCalled()
  await act(async () => {
    release()
    await gate
  })
  await waitForUI(() => {
    expect(done).toHaveBeenCalled()
    expect(button('Connect team/repo').disabled).toBe(true)
  })
  act(() => {
    button('Connect team/repo').click()
  })
  expect(api.requestsTo('POST', completePath)).toHaveLength(1)
  act(() => {
    button('Done').click()
  })
  expect(close).toHaveBeenCalled()
})
