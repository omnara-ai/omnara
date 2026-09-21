/** @vitest-environment happy-dom */
import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, schemas } from '@omnara/sdk'
import { getProjectAppQueryKey } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { type FakeApi, fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId, projectApp } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, waitForUI } from '@/test/secret-editor'

import { ConnectSlackForm } from './ConnectSlackForm'
import { ProjectAppForm } from './ProjectAppForm'
import { ProjectAppSetup } from './ProjectAppSetup'

const orgId = fakeId('org'),
  projectId = fakeId('proj')
const path = `/api/v1/orgs/${orgId}/projects/${projectId}`
let root: Root, container: HTMLDivElement, cache: QueryClient, restore: () => void
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
  cache = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  cache.clear()
  container.remove()
  restore()
  vi.restoreAllMocks()
})
function render(api: FakeApi, node: ReactNode) {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  const rerender = (next: ReactNode) => {
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>{next}</QueryClientProvider>
        </OmnaraClientProvider>,
      )
    })
  }
  rerender(node)
  return { client, rerender }
}
async function submit() {
  await act(async () => {
    document
      .querySelector('form')
      ?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await Promise.resolve()
  })
}

it.each(['slack_thread', 'github_pr', 'discord_thread'] as const)(
  'creates a disconnected %s app before setup',
  async (appType) => {
    const api = fakeApi([
      {
        method: 'POST',
        path: path + '/apps',
        respond: ({ body }) =>
          Response.json(projectApp(schemas.zSaveProjectAppRequest.parse(body)), {
            status: 201,
          }),
      },
    ])
    const onSaved = vi.fn()
    render(
      api,
      <ProjectAppForm orgId={orgId} projectId={projectId} appType={appType} onSaved={onSaved} />,
    )
    await enter('App name', 'engineering-2')
    await submit()
    await waitForUI(() => {
      expect(onSaved).toHaveBeenCalledWith(
        expect.objectContaining({ state: 'disconnected', name: 'engineering-2' }),
      )
    })
    expect(api.requests).toHaveLength(1)
    expect(api.requests[0]?.body).toEqual({
      name: 'engineering-2',
      app_type: appType,
      settings: {},
    })
  },
)

it('validates immutable names before any request and retries duplicate-name errors', async () => {
  let attempts = 0
  const api = fakeApi([
    {
      method: 'POST',
      path: path + '/apps',
      respond: ({ body }) =>
        ++attempts === 1
          ? jsonResponse({ code: 'conflict', error: 'App name already exists' }, 409)
          : Response.json(projectApp({ ...schemas.zSaveProjectAppRequest.parse(body) }), {
              status: 201,
            }),
    },
  ])
  const onSaved = vi.fn()
  render(
    api,
    <ProjectAppForm orgId={orgId} projectId={projectId} appType="slack_thread" onSaved={onSaved} />,
  )
  await enter('App name', 'with space')
  await submit()
  expect(container.textContent).toContain('1–32')
  expect(api.requests).toHaveLength(0)
  await enter('App name', 'engineering')
  await submit()
  await waitForUI(() => {
    expect(container.textContent).toContain('App name already exists')
  })
  await enter('App name', 'engineering-2')
  await submit()
  await waitForUI(() => {
    expect(onSaved).toHaveBeenCalled()
  })
})

it.each(['github_pr', 'discord_thread'] as const)(
  'retries %s verification using the already-saved credential',
  async (appType) => {
    const app = projectApp({ app_type: appType })
    let attempts = 0
    const api = fakeApi([
      {
        method: 'POST',
        path: `/api/v1/orgs/${orgId}/secrets`,
        respond: () =>
          Response.json(
            {
              id: fakeId('sec'),
              org_id: orgId,
              name: 'Credentials',
              owner: { kind: 'project', project_id: projectId },
              kind: appType === 'github_pr' ? 'github_app_credentials' : 'generic',
              management_kind: 'tenant',
              metadata: {},
              current_version_number: 1,
              payload_keys: [],
              created_at: app.created_at,
              updated_at: app.updated_at,
            },
            { status: 201 },
          ),
      },
      {
        method: 'POST',
        path: path + '/apps/' + app.id + '/setup',
        respond: ({ body }) => {
          const setup = schemas.zConfigureProjectAppRequest.parse(body)
          return ++attempts === 1
            ? jsonResponse({ code: 'conflict', error: 'Verification failed; try again' }, 409)
            : Response.json({ ...app, ...setup, state: 'active', setup_revision: 2 })
        },
      },
      { method: 'GET', path: path + '/apps/' + app.id, respond: () => Response.json(app) },
    ])
    const onSaved = vi.fn()
    render(
      api,
      <ProjectAppSetup
        orgId={orgId}
        projectId={projectId}
        app={app}
        onSaved={onSaved}
        onCancel={vi.fn()}
      />,
    )
    await enter(appType === 'github_pr' ? 'GitHub App ID' : 'Discord Application ID', '111')
    await enter(appType === 'github_pr' ? 'Installation ID' : 'Bot User ID', '222')
    if (appType === 'github_pr') {
      await enter('RSA private key (PEM)', 'test-key')
      await enter('Webhook secret', 'signature')
    } else {
      await enter('Bot token', 'token')
      await enter('Interaction public key', 'ab'.repeat(32))
      await enter('Gateway shards', '4')
    }
    await submit()
    await waitForUI(() => {
      expect(container.textContent).toContain('Verification failed; try again')
    })
    expect(container.textContent).toContain('Credentials saved')
    await submit()
    await waitForUI(() => {
      expect(onSaved).toHaveBeenCalledWith(expect.objectContaining({ id: app.id, state: 'active' }))
    })
    expect(api.requestsTo('POST', `/api/v1/orgs/${orgId}/secrets`)).toHaveLength(1)
    expect(api.requestsTo('POST', path + '/apps/' + app.id + '/setup')).toHaveLength(2)
    expect(api.requestsTo('POST', path + '/apps')).toHaveLength(0)
    expect(api.requestsTo('POST', path + '/apps/' + app.id + '/setup').at(-1)?.body).toMatchObject({
      expected_setup_revision: 1,
      credential_secret_id: fakeId('sec'),
      provider_tenant_id: '111',
      provider_account_ref: '222',
    })
    if (appType === 'discord_thread')
      expect(
        api.requestsTo('POST', path + '/apps/' + app.id + '/setup').at(-1)?.body,
      ).toMatchObject({
        provider_config: { shard_count: 4, public_key: 'ab'.repeat(32) },
      })
  },
)

it('keeps the displayed account and endpoint aligned when another tab connects the app', async () => {
  const app = projectApp({ app_type: 'discord_thread' })
  const props = { orgId, projectId, onSaved: vi.fn() }
  const { rerender } = render(fakeApi([]), <ProjectAppSetup {...props} app={app} />)
  const value = (id: string) => container.querySelector<HTMLInputElement>(`#${id}`)?.value
  expect(value('provider-endpoint')).toBe('')
  expect(button('Copy').disabled).toBe(true)
  await enter('Discord Application ID', '111')
  await enter('Bot User ID', '222')
  expect(value('provider-endpoint')).toBe(
    'https://omnara.test/api/integrations/discord/111/interactions',
  )
  expect(button('Copy').disabled).toBe(false)
  rerender(
    <ProjectAppSetup
      {...props}
      app={{ ...app, state: 'active', provider_tenant_id: '333', provider_account_ref: '444' }}
    />,
  )
  expect(value('provider-tenant')).toBe('333')
  expect(value('provider-account')).toBe('444')
  expect(value('provider-endpoint')).toBe(
    'https://omnara.test/api/integrations/discord/333/interactions',
  )
})

it('blocks a stale edit until explicitly reloaded', async () => {
  const app = projectApp({ app_type: 'github_pr' })
  const next = { ...app, updated_at: '2026-09-20T00:00:00Z' }
  const api = fakeApi([])
  const props = { orgId, projectId, appType: 'github_pr' as const, onSaved: vi.fn() }
  const { rerender } = render(api, <ProjectAppForm {...props} app={app} />)
  rerender(<ProjectAppForm {...props} app={next} />)
  expect(container.textContent).toContain('App changed. Reload settings')
  await submit()
  expect(api.requests).toHaveLength(0)
  act(() => {
    button('Reload settings').click()
  })
  expect(container.textContent).not.toContain('App changed.')
})

it('keeps cancellation disabled during creation and ignores completion after unmount', async () => {
  let release!: (response: Response) => void
  const api = fakeApi([
    {
      method: 'POST',
      path: path + '/apps',
      respond: () =>
        new Promise((resolve) => {
          release = resolve
        }),
    },
  ])
  const onSaved = vi.fn(),
    onCancel = vi.fn()
  const { rerender } = render(
    api,
    <ProjectAppForm
      orgId={orgId}
      projectId={projectId}
      appType="slack_thread"
      onSaved={onSaved}
      onCancel={onCancel}
    />,
  )
  await submit()
  await waitForUI(() => {
    expect(api.requests).toHaveLength(1)
  })
  await waitForUI(() => {
    expect(button('Cancel').disabled).toBe(true)
  })
  act(() => {
    button('Cancel').click()
  })
  expect(onCancel).not.toHaveBeenCalled()
  rerender(null)
  await act(async () => {
    release(Response.json(projectApp(), { status: 201 }))
    await Promise.resolve()
  })
  expect(onSaved).not.toHaveBeenCalled()
})

it('posts Slack OAuth credentials to the named app and keeps setup failures actionable', async () => {
  const app = projectApp({ provider_tenant_id: 'T123', provider_account_ref: 'A123' })
  const api = fakeApi([
    {
      method: 'POST',
      path: path + '/apps/' + app.id + '/oauth/setup',
      respond: () => jsonResponse({ code: 'conflict', error: 'Try authorization again' }, 409),
    },
  ])
  render(api, <ConnectSlackForm app={app} orgId={orgId} projectId={projectId} />)
  await enter('Client ID', 'client')
  await enter('Client secret', 'secret')
  await enter('Signing secret', 'signature')
  await submit()
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Try authorization again')
  })
  expect(api.requests[0]?.body).toEqual({
    client_id: 'client',
    client_secret: 'secret',
    signing_secret: 'signature',
    return_to: window.location.pathname,
  })
})

it('completes OAuth only for the exact app flow, not a previous active flow', async () => {
  const app = projectApp({ provider_tenant_id: 'T123', provider_account_ref: 'A123' })
  const flowId = fakeId('ioaf')
  const setup = {
    app_id: app.id,
    setup_revision: app.setup_revision,
    provider: 'slack',
    flow_id: flowId,
    oauth_url: 'https://slack.com/oauth/v2/authorize',
    redirect_uri: 'https://omnara.test/callback',
    events_url: 'https://omnara.test/events',
    actions_url: 'https://omnara.test/actions',
    expires_at: new Date(Date.now() + 600_000).toISOString(),
  }
  const api = fakeApi([
    {
      method: 'POST',
      path: path + '/apps/' + app.id + '/oauth/setup',
      respond: () => Response.json(setup, { status: 201 }),
    },
    {
      method: 'GET',
      path: path + '/apps/' + app.id,
      respond: () =>
        Response.json({
          ...app,
          state: 'active',
          setup_revision: app.setup_revision,
          last_oauth_flow_id: `ioaf_${'b'.repeat(26)}`,
        }),
    },
  ])
  const onConnected = vi.fn()
  const { client } = render(
    api,
    <ConnectSlackForm app={app} orgId={orgId} projectId={projectId} onConnected={onConnected} />,
  )
  await enter('Client ID', 'client')
  await enter('Client secret', 'secret')
  await enter('Signing secret', 'signature')
  await submit()
  await waitForUI(() => {
    expect(api.requestsTo('GET', path + '/apps/' + app.id)).toHaveLength(1)
  })
  expect(document.querySelector('a')?.getAttribute('href')).toBe(setup.oauth_url)
  expect(onConnected).not.toHaveBeenCalled()
  const key = getProjectAppQueryKey({
    path: { orgID: orgId, projectID: projectId, appID: app.id },
    client,
  })
  act(() => {
    cache.setQueryData(key, { ...app, state: 'disconnected', last_oauth_flow_id: flowId })
  })
  expect(onConnected).not.toHaveBeenCalled()
  act(() => {
    cache.setQueryData(key, {
      ...app,
      state: 'active',
      setup_revision: app.setup_revision + 1,
      last_oauth_flow_id: flowId,
    })
  })
  await waitForUI(() => {
    expect(onConnected).toHaveBeenCalledOnce()
  })
})

it('keeps the reconnect credential selected when its fallback option is replaced by fetched secrets', async () => {
  const secretID = fakeId('sec')
  const app = projectApp({
    app_type: 'github_pr',
    credential_secret_id: secretID,
    provider_tenant_id: '111',
    provider_account_ref: '222',
  })
  let resolveSecrets: (response: Response) => void = () => {
    throw new Error('Secret response is not ready')
  }
  const pending = new Promise<Response>((resolve) => {
    resolveSecrets = resolve
  })
  const api = fakeApi([
    { method: 'GET', path: path + '/secrets', respond: () => pending },
    {
      method: 'POST',
      path: path + '/apps/' + app.id + '/setup',
      respond: () => Response.json({ ...app, state: 'active', setup_revision: 2 }),
    },
  ])
  const onSaved = vi.fn()
  render(
    api,
    <ProjectAppSetup
      orgId={orgId}
      projectId={projectId}
      app={app}
      onSaved={onSaved}
      onCancel={vi.fn()}
    />,
  )
  const selected = () => container.querySelector<HTMLSelectElement>('#saved-secret')?.value
  expect(container.textContent).toContain('Current credential')
  expect(selected()).toBe(secretID)
  await act(async () => {
    resolveSecrets(
      Response.json({
        data: [
          {
            secret: {
              id: secretID,
              org_id: orgId,
              name: 'Saved GitHub credential',
              kind: 'github_app_credentials',
              owner: { kind: 'project', project_id: projectId },
              management_kind: 'tenant',
              metadata: {},
              current_version_number: 1,
              payload_keys: [],
              created_at: app.created_at,
              updated_at: app.updated_at,
            },
            availability: { source: 'direct', project_id: projectId },
          },
        ],
        next_cursor: null,
      }),
    )
    await pending
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Saved GitHub credential')
  })
  expect(selected()).toBe(secretID)
  await submit()
  await waitForUI(() => {
    expect(onSaved).toHaveBeenCalled()
  })
  expect(api.requestsTo('POST', path + '/apps/' + app.id + '/setup')[0]?.body).toMatchObject({
    credential_secret_id: secretID,
  })
})
