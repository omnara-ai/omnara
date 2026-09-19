/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import {
  type AgentProfile,
  createOmnaraClient,
  type IntegrationConnection,
  type ProfileAppProvider,
  type ProjectApp,
  type SaveProjectAppRequest,
  schemas,
} from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { type FakeApi, fakeApi, type FakeRoute, jsonResponse, neverResponds } from '@/test/fake-api'
import { agentConfigModel, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, submit, waitForUI } from '@/test/secret-editor'

import { ConnectSlackDialog } from './ConnectSlackDialog'
import { ProjectAppForm } from './ProjectAppForm'

const orgId = fakeId('org'),
  projectId = fakeId('proj'),
  connectionId = fakeId('iin')
const path = `/api/v1/orgs/${orgId}/projects/${projectId}`
const credentialPath = `/api/v1/orgs/${orgId}/secrets`
const now = '2026-09-18T00:00:00Z'
function profile(letter: string, name: string): AgentProfile {
  return {
    id: `aprf_${letter.repeat(26)}`,
    name,
    org_id: orgId,
    project_id: projectId,
    current_generation: 1,
    current_config_id: fakeId('acfg'),
    current_config: {
      id: fakeId('acfg'),
      org_id: orgId,
      project_id: projectId,
      model: agentConfigModel(),
      effective_definition_hash: 'hash',
      created_at: now,
    },
    created_at: now,
    updated_at: now,
  }
}
const support = profile('a', 'Support'),
  reviews = profile('b', 'Reviews')
function connection(provider: ProfileAppProvider): IntegrationConnection {
  return {
    id: connectionId,
    org_id: orgId,
    project_id: projectId,
    provider,
    provider_tenant_id: provider === 'slack' ? 'T123' : '111',
    provider_account_ref: '222',
    provider_agent_display_name: 'Shared bot',
    state: 'active',
    provider_config: { public_key: 'ab'.repeat(32) },
    created_at: now,
    updated_at: now,
  }
}
function saved(request: SaveProjectAppRequest): ProjectApp {
  return {
    ...request,
    enabled: request.enabled ?? true,
    id: fakeId('app'),
    project_id: projectId,
    created_at: now,
    updated_at: now,
  }
}
function routes(provider: ProfileAppProvider): FakeRoute[] {
  return [
    {
      method: 'GET',
      path: path + '/integration-connections',
      respond: () => Response.json({ data: [connection(provider)], next_cursor: null }),
    },
    {
      method: 'GET',
      path: path + '/integration-connections/' + connectionId,
      respond: () => Response.json(connection(provider)),
    },
    {
      method: 'GET',
      path: path + '/secrets',
      respond: () => Response.json({ data: [], next_cursor: null }),
    },
    {
      method: 'GET',
      path: path + '/agent-profiles',
      respond: ({ url }) =>
        Response.json({
          data: url.searchParams.has('cursor') ? [reviews] : [support],
          next_cursor: url.searchParams.has('cursor') ? null : 'page-two',
        }),
    },
    ...[support, reviews].map((item) => ({
      method: 'GET',
      path: path + '/agent-profiles/' + item.id,
      respond: () => Response.json(item),
    })),
    {
      method: 'POST',
      path: path + '/apps',
      respond: ({ body }) =>
        Response.json(saved(schemas.zSaveProjectAppRequest.parse(body)), { status: 201 }),
    },
    {
      method: 'PUT',
      path: path + '/apps/' + fakeId('app'),
      respond: ({ body }) => Response.json(saved(schemas.zSaveProjectAppRequest.parse(body))),
    },
  ]
}
let root: Root, container: HTMLDivElement, cache: QueryClient, restore: () => void
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
  cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  cache.clear()
  container.remove()
  restore()
})
function render(api: FakeApi, node: ReactNode) {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={cache}>{node}</QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
}
function toggle(name: string) {
  const input = document.querySelector<HTMLInputElement>(`input[name="${name}"]`)
  if (!input) throw new Error(`Missing checkbox ${name}`)
  act(() => {
    input.click()
  })
}
function select(id: string, value: string) {
  const input = document.getElementById(id)
  if (!(input instanceof HTMLSelectElement)) throw new Error(`Missing select ${id}`)
  act(() => {
    input.value = value
    input.dispatchEvent(new Event('change', { bubbles: true }))
  })
}
async function openProfiles(single = false) {
  if (single) {
    act(() => document.getElementById('app-profiles')?.click())
    return
  }
  await act(async () => {
    const input = document.querySelector<HTMLInputElement>('input[role="combobox"]')
    if (!input) throw new Error('Missing profile picker')
    input.focus()
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    await Promise.resolve()
  })
}
async function chooseProfile(name: string, single = false) {
  await openProfiles(single)
  await waitForUI(() => {
    expect(
      [...document.querySelectorAll('[role="option"]')].some((item) => item.textContent === name),
    ).toBe(true)
  })
  act(() =>
    [...document.querySelectorAll<HTMLElement>('[role="option"]')]
      .find((item) => item.textContent === name)
      ?.click(),
  )
}

it.each(['slack', 'github', 'discord'] as const)(
  'creates a %s app with no launcher, profiles or concrete scope',
  async (provider) => {
    const api = fakeApi([
      {
        method: 'GET',
        path: path + '/agent-profiles',
        respond: () => Response.json({ data: [], next_cursor: null }),
      },
      ...routes(provider),
    ])
    const onSaved = vi.fn()
    render(
      api,
      <ProjectAppForm
        orgId={orgId}
        projectId={projectId}
        provider={provider}
        initialConnectionId={connectionId}
        onSaved={onSaved}
      />,
    )
    await waitForUI(() => {
      expect(document.getElementById('app-profiles')).not.toBeNull()
    })
    expect(button('Create app').disabled).toBe(true)
    await submit()
    expect(api.requestsTo('POST', path + '/apps')).toHaveLength(0)
    toggle('launcher')
    expect(document.getElementById('app-profiles')).toBeNull()
    expect(document.body.textContent).toContain('supply a conversation scope')
    toggle('listen')
    await enter('App setup name', 'Project helper')
    await submit()
    await waitForUI(() => {
      expect(onSaved).toHaveBeenCalledWith(
        expect.objectContaining({ name: 'Project helper', id: fakeId('app') }),
      )
    })
    const body = schemas.zSaveProjectAppRequest.parse(
      api.requestsTo('POST', path + '/apps')[0]?.body,
    )
    expect(body.settings.launcher).toBeUndefined()
    expect(body.settings.resource.scope).toBeUndefined()
    expect(body.settings.resource.listener).toBeUndefined()
    expect(body.settings.resource.connection).toBe(connectionId)
    expect(api.requests.filter((request) => request.method !== 'GET')).toHaveLength(1)
  },
)

it.each([
  { provider: 'slack', scopeKind: 'workspace', trigger: 'mention' },
  { provider: 'slack', scopeKind: 'channel', trigger: 'mention' },
  { provider: 'discord', scopeKind: 'channel', trigger: 'mention' },
  { provider: 'github', scopeKind: 'repository', trigger: 'pull_request_opened' },
  { provider: 'github', scopeKind: 'repository', trigger: 'mention' },
] as const)(
  'creates $provider $scopeKind / $trigger launch settings',
  async ({ provider, scopeKind, trigger }) => {
    const api = fakeApi(routes(provider))
    const onSaved = vi.fn()
    render(
      api,
      <ProjectAppForm
        orgId={orgId}
        projectId={projectId}
        provider={provider}
        initialConnectionId={connectionId}
        onSaved={onSaved}
      />,
    )
    await waitForUI(() => {
      expect(document.querySelector('#app-connection')?.textContent).toContain('Shared bot')
    })
    expect(button('Create app').disabled).toBe(true)
    if (provider === 'slack') select('app-launch-scope', scopeKind)
    if (provider === 'github') select('app-trigger', trigger)
    const scopeRef = scopeKind === 'workspace' ? 'T123' : provider === 'slack' ? 'C123' : '333'
    if (scopeKind !== 'workspace')
      await enter(provider === 'github' ? 'Repository ID' : 'Channel ID', scopeRef)
    await chooseProfile('Support', provider === 'github')
    if (provider !== 'github') {
      await openProfiles()
      await waitForUI(() => {
        expect(button('Load more results')).toBeDefined()
      })
      act(() => {
        button('Load more results').click()
      })
      await chooseProfile('Reviews')
    }
    await submit()
    await waitForUI(() => {
      expect(onSaved).toHaveBeenCalled()
    })
    expect(api.requestsTo('POST', path + '/apps')[0]?.body).toMatchObject({
      settings: {
        resource: { connection: connectionId },
        launcher: {
          trigger,
          scope_kind: scopeKind,
          scope_ref: scopeRef,
          slots:
            provider === 'github'
              ? [{ key: 'default', agent_profile_id: support.id }]
              : [
                  { key: 'default', agent_profile_id: support.id },
                  { key: 'profile_2', agent_profile_id: reviews.id },
                ],
        },
      },
    })
  },
)

it.each([
  { provider: 'github', failure: 'scope' },
  { provider: 'discord', failure: 'scope' },
  { provider: 'github', failure: 'app' },
  { provider: 'discord', failure: 'app' },
  { provider: 'github', failure: 'connection' },
  { provider: 'discord', failure: 'connection' },
] as const)(
  'retries $provider $failure failure without duplicating saved steps',
  async ({ provider, failure }) => {
    let connectionAttempts = 0,
      appAttempts = 0
    const api = fakeApi([
      {
        method: 'POST',
        path: credentialPath,
        respond: () =>
          Response.json(
            {
              id: fakeId('sec'),
              org_id: orgId,
              name: 'Credentials',
              owner: { kind: 'project', project_id: projectId },
              kind: provider === 'github' ? 'github_app_credentials' : 'generic',
              management_kind: 'tenant',
              metadata: {},
              current_version_number: 1,
              payload_keys: [],
              created_at: now,
              updated_at: now,
            },
            { status: 201 },
          ),
      },
      {
        method: 'POST',
        path: path + '/integration-connections',
        respond: () =>
          ++connectionAttempts === 1 && failure === 'connection'
            ? jsonResponse({ code: 'conflict', error: 'Connection capacity reached' }, 409)
            : Response.json(connection(provider), { status: 201 }),
      },
      {
        method: 'POST',
        path: path + '/apps',
        respond: ({ body }) =>
          ++appAttempts === 1 && failure === 'app'
            ? jsonResponse({ code: 'conflict', error: 'Project app capacity reached' }, 409)
            : Response.json(saved(schemas.zSaveProjectAppRequest.parse(body)), { status: 201 }),
      },
      ...routes(provider),
    ])
    const onSaved = vi.fn()
    render(
      api,
      <ProjectAppForm orgId={orgId} projectId={projectId} provider={provider} onSaved={onSaved} />,
    )
    if (failure !== 'scope') toggle('launcher')
    else {
      await enter(provider === 'github' ? 'Repository ID' : 'Channel ID', 'invalid')
      await chooseProfile('Support', provider === 'github')
    }
    await enter(provider === 'github' ? 'GitHub App ID' : 'Discord Application ID', '111')
    await enter(provider === 'github' ? 'GitHub Installation ID' : 'Discord bot User ID', '222')
    if (provider === 'github') {
      await enter('RSA private key (PEM)', 'test-key')
      await enter('Webhook secret', 'signature')
    } else {
      await enter('Bot token', 'token')
      await enter('Gateway shard count', '4')
    }
    await submit()
    await waitForUI(() => {
      expect(document.body.textContent).toContain(
        failure === 'app'
          ? 'Project app capacity reached'
          : failure === 'scope'
            ? 'Enter a positive ID without leading zeros.'
            : 'Connection capacity reached',
      )
    })
    expect(onSaved).not.toHaveBeenCalled()
    expect(document.querySelector<HTMLSelectElement>('#app-connection')?.disabled).toBe(
      failure !== 'scope',
    )
    if (failure === 'scope')
      expect(api.requests.filter((request) => request.method === 'POST')).toHaveLength(0)
    if (failure === 'connection')
      expect(document.querySelector<HTMLInputElement>('#provider-tenant')?.readOnly).toBe(true)
    if (failure === 'scope')
      await enter(provider === 'github' ? 'Repository ID' : 'Channel ID', '333')
    await submit()
    await waitForUI(() => {
      expect(onSaved).toHaveBeenCalled()
    })
    expect(api.requestsTo('POST', credentialPath)).toHaveLength(1)
    expect(connectionAttempts).toBe(failure === 'connection' ? 2 : 1)
    expect(appAttempts).toBe(failure === 'app' ? 2 : 1)
  },
)

const advanced: ProjectApp = saved({
  name: 'Advanced app',
  enabled: false,
  settings: {
    resource: {
      definition: 'omnara.github',
      connection: connectionId,
      enabled: false,
      scope: { github: { repository_id: 123, pull_request: 4 } },
      follow: { replies: false },
      tools: {
        github_read: {
          enabled: false,
          deferred: true,
          permission: { mode: 'always_allow', parameters: { retain: true } },
        },
        custom_tool: { type: 'custom', description: 'Kept', input_schema: { type: 'object' } },
      },
      listener: { events: ['review_comment'] },
      mcp: {
        custom: {
          url: 'https://tools.example/mcp',
          auth: { type: 'bearer', secret_id: fakeId('sec') },
          tools: { inspect: { permission: { mode: 'always_ask' } } },
        },
      },
    },
    launcher: {
      trigger: 'mention',
      scope_kind: 'pull_request',
      scope_ref: '123#4',
      slots: [
        { key: 'named-reviewer', agent_profile_id: support.id },
        { key: 'other-reviewer', agent_profile_id: support.id },
        { key: 'existing', agent_id: fakeId('agt') },
      ],
    },
  },
})

it('renames an advanced app and preserves every other setting across failed update retries', async () => {
  let attempts = 0
  const api = fakeApi([
    {
      method: 'PUT',
      path: path + '/apps/' + advanced.id,
      respond: ({ body }) =>
        ++attempts === 1
          ? jsonResponse({ code: 'conflict', error: 'Retry this update' }, 409)
          : Response.json(saved(schemas.zSaveProjectAppRequest.parse(body))),
    },
    ...routes('github'),
  ])
  const onSaved = vi.fn()
  render(
    api,
    <ProjectAppForm
      orgId={orgId}
      projectId={projectId}
      provider="github"
      app={advanced}
      onSaved={onSaved}
    />,
  )
  expect(document.querySelector('#app-connection')).toBeNull()
  expect(document.body.textContent).toContain('3 saved launch slots')
  expect(document.body.textContent).toContain('pull_request / 123#4')
  await enter('App setup name', 'Renamed app')
  await submit()
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Retry this update')
  })
  await submit()
  await waitForUI(() => {
    expect(onSaved).toHaveBeenCalled()
  })
  const updates = api.requestsTo('PUT', path + '/apps/' + advanced.id)
  expect(updates).toHaveLength(2)
  expect(updates[0]?.body).toEqual(updates[1]?.body)
  expect(updates[1]?.body).toEqual({
    name: 'Renamed app',
    enabled: false,
    settings: advanced.settings,
  })
  expect(api.requests.filter((request) => request.method !== 'GET')).toHaveLength(2)
})

it('turns off an advanced launcher deliberately while retaining reusable defaults', async () => {
  const api = fakeApi(routes('github')),
    onSaved = vi.fn()
  render(
    api,
    <ProjectAppForm
      orgId={orgId}
      projectId={projectId}
      provider="github"
      app={advanced}
      onSaved={onSaved}
    />,
  )
  toggle('launcher')
  expect(document.body.textContent).toContain('removes this launcher and all of its launch slots')
  await submit()
  await waitForUI(() => {
    expect(onSaved).toHaveBeenCalled()
  })
  expect(api.requestsTo('PUT', path + '/apps/' + advanced.id)[0]?.body).toEqual({
    name: advanced.name,
    enabled: false,
    settings: { resource: advanced.settings.resource },
  })
})

it('calls the supplied Slack connection action without requiring a profile', () => {
  const onConnectSlack = vi.fn()
  render(
    fakeApi(routes('slack')),
    <ProjectAppForm
      orgId={orgId}
      projectId={projectId}
      provider="slack"
      onSaved={vi.fn()}
      onConnectSlack={onConnectSlack}
    />,
  )
  act(() => {
    button('Connect a Slack app through OAuth').click()
  })
  expect(onConnectSlack).toHaveBeenCalledOnce()
})

it('sends Slack OAuth credentials to project setup and keeps errors actionable', async () => {
  const setupPath = path + '/integration-oauth/setup'
  const api = fakeApi([
    {
      method: 'POST',
      path: setupPath,
      respond: () =>
        jsonResponse(
          { code: 'invalid_request', error: 'Slack client credentials are invalid' },
          400,
        ),
    },
  ])
  render(
    api,
    <ConnectSlackDialog open onOpenChange={vi.fn()} orgId={orgId} projectId={projectId} />,
  )
  act(() => document.querySelector<HTMLInputElement>('input[type="checkbox"]')?.click())
  await enter('Client ID', 'client-id')
  await enter('Client secret', 'client-secret')
  await enter('Signing secret', 'signing-secret')
  act(() => {
    button('Continue').click()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Slack client credentials are invalid')
  })
  expect(api.requestsTo('POST', setupPath)[0]?.body).toEqual({
    provider: 'slack',
    client_id: 'client-id',
    client_secret: 'client-secret',
    signing_secret: 'signing-secret',
    return_to: window.location.pathname,
  })
  expect(document.body.textContent).toContain('Existing Omnara apps and their settings are kept')
})

it('blocks saving after a refetch and deliberately reloads the full edit baseline', async () => {
  const api = fakeApi(routes('github')),
    onSaved = vi.fn()
  const renderApp = (app: ProjectApp) => {
    render(
      api,
      <ProjectAppForm
        orgId={orgId}
        projectId={projectId}
        provider="github"
        app={app}
        onSaved={onSaved}
      />,
    )
  }
  renderApp(advanced)
  await enter('App setup name', 'My unsaved rename')
  const refreshed: ProjectApp = {
    ...advanced,
    name: 'Updated elsewhere',
    updated_at: '2026-09-19T01:00:00Z',
    settings: {
      ...advanced.settings,
      resource: {
        ...advanced.settings.resource,
        tools: { ...advanced.settings.resource.tools, github_reply: { deferred: true } },
      },
    },
  }
  renderApp(refreshed)
  expect(document.querySelector<HTMLInputElement>('#app-name')?.value).toBe('My unsaved rename')
  expect(document.body.textContent).toContain('App changed. Reload settings before saving.')
  expect(button('Save changes').disabled).toBe(true)
  await act(async () => {
    document
      .querySelector('form')
      ?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await Promise.resolve()
  })
  expect(api.requestsTo('PUT', path + '/apps/' + advanced.id)).toHaveLength(0)
  act(() => {
    button('Reload settings').click()
  })
  expect(document.querySelector<HTMLInputElement>('#app-name')?.value).toBe('Updated elsewhere')
  expect(document.querySelector<HTMLInputElement>('input[value="github_reply"]')?.checked).toBe(
    true,
  )
  await enter('App setup name', 'My final rename')
  await submit()
  await waitForUI(() => {
    expect(onSaved).toHaveBeenCalled()
  })
  expect(api.requestsTo('PUT', path + '/apps/' + advanced.id)[0]?.body).toEqual({
    name: 'My final rename',
    enabled: false,
    settings: refreshed.settings,
  })
})

it('shows a readable name error and keeps API validation details out of the form', async () => {
  const api = fakeApi(routes('github'))
  render(
    api,
    <ProjectAppForm
      orgId={orgId}
      projectId={projectId}
      provider="github"
      app={advanced}
      onSaved={vi.fn()}
    />,
  )
  await enter('App setup name', '')
  expect(document.body.textContent).toContain('Enter an app name')
  expect(document.body.textContent).not.toContain('"code"')
  expect(document.body.textContent).not.toContain('"path"')
  expect(button('Save changes').disabled).toBe(true)
  expect(document.querySelector('input[name="enabled"]')).toBeNull()
})

it.each(['off-page', 'wrong-provider', 'inactive'] as const)(
  'gates Slack settings until the returned connection is active (%s)',
  async (outcome) => {
    const selected = {
      ...connection(outcome === 'wrong-provider' ? 'discord' : 'slack'),
      state: outcome === 'inactive' ? 'disconnected' : 'active',
    }
    const api = fakeApi([
      {
        method: 'GET',
        path: path + '/integration-connections',
        respond: () => Response.json({ data: [], next_cursor: 'next-page' }),
      },
      {
        method: 'GET',
        path: path + '/integration-connections/' + connectionId,
        respond: () => Response.json(selected),
      },
      ...routes('slack'),
    ])
    render(
      api,
      <ProjectAppForm
        orgId={orgId}
        projectId={projectId}
        provider="slack"
        initialConnectionId={connectionId}
        onSaved={vi.fn()}
        onConnectSlack={vi.fn()}
      />,
    )
    expect(document.querySelector('#app-name')).toBeNull()
    expect(document.querySelector('#app-profiles')).toBeNull()
    await waitForUI(() => {
      expect(api.requestsTo('GET', path + '/integration-connections/' + connectionId)).toHaveLength(
        1,
      )
      if (outcome === 'off-page') expect(document.querySelector('#app-name')).not.toBeNull()
      else
        expect(document.body.textContent).toContain(
          outcome === 'wrong-provider' ? 'Choose a Slack connection.' : 'Shared bot',
        )
    })
    if (outcome !== 'off-page') {
      expect(document.querySelector('#app-name')).toBeNull()
      expect(button('Create app').disabled).toBe(true)
      await submit()
      expect(api.requestsTo('POST', path + '/apps')).toHaveLength(0)
    } else {
      expect(document.querySelector<HTMLSelectElement>('#app-connection')?.value).toBe(connectionId)
    }
  },
)

it('owns cancel and prevents unmounting through that action while a save is pending', async () => {
  const onCancel = vi.fn()
  const api = fakeApi([
    { method: 'PUT', path: path + '/apps/' + advanced.id, respond: neverResponds },
    ...routes('github'),
  ])
  render(
    api,
    <ProjectAppForm
      orgId={orgId}
      projectId={projectId}
      provider="github"
      app={advanced}
      onSaved={vi.fn()}
      onCancel={onCancel}
    />,
  )
  expect(button('Cancel').disabled).toBe(false)
  act(() => {
    button('Cancel').click()
  })
  expect(onCancel).toHaveBeenCalledOnce()
  await act(async () => {
    document
      .querySelector('form')
      ?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await Promise.resolve()
  })
  await waitForUI(() => {
    expect(button('Cancel').disabled).toBe(true)
  })
  act(() => {
    button('Cancel').click()
  })
  expect(onCancel).toHaveBeenCalledOnce()
})

it('does not call onSaved when creation resolves after navigating away', async () => {
  let finish: ((response: Response) => void) | undefined
  const api = fakeApi([
    {
      method: 'POST',
      path: path + '/apps',
      respond: () =>
        new Promise<Response>((resolve) => {
          finish = resolve
        }),
    },
    ...routes('github'),
  ])
  const onSaved = vi.fn()
  render(
    api,
    <ProjectAppForm
      orgId={orgId}
      projectId={projectId}
      provider="github"
      initialConnectionId={connectionId}
      onSaved={onSaved}
    />,
  )
  toggle('launcher')
  await waitForUI(() => {
    expect(button('Create app').disabled).toBe(false)
  })
  act(() => {
    document
      .querySelector('form')
      ?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  })
  await waitForUI(() => {
    expect(api.requestsTo('POST', path + '/apps')).toHaveLength(1)
  })
  act(() => {
    root.render(<p>Another page</p>)
  })
  if (!finish) throw new Error('Missing pending create request')
  finish(
    Response.json(
      saved(schemas.zSaveProjectAppRequest.parse(api.requestsTo('POST', path + '/apps')[0]?.body)),
      { status: 201 },
    ),
  )
  await waitForUI(() => {
    expect(cache.isMutating()).toBe(0)
  })
  expect(onSaved).not.toHaveBeenCalled()
  expect(container.textContent).toBe('Another page')
})
