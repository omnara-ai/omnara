/** @vitest-environment happy-dom */

import { getProjectAppQueryKey, listAppDefinitionsQueryKey } from '@omnara/sdk/tanstack'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { z } from 'zod'

import { ProjectAppCreateSetup } from '@/routes/CreateProjectAppPage'
import { ProjectAppDetail } from '@/routes/ProjectAppPage'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { appDefinition, fakeId, projectApp } from '@/test/fixtures'
import { renderProjectApp } from '@/test/project-app-render'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, waitForUI } from '@/test/secret-editor'

import { AppCatalog } from './AppCatalog'
import { RemoveProjectAppButton } from './ProjectAppActions'
import { ProjectAppAdvanced } from './ProjectAppAdvanced'
import { ProjectAppsList } from './ProjectAppsList'
import { useProjectAppActions } from './useProjectAppActions'

const orgId = fakeId('org'),
  projectId = fakeId('proj')
const projectPath = `/api/v1/orgs/${orgId}/projects/${projectId}`
const savedApp = projectApp({
  app_type: 'github_pr',
  name: 'reviewer',
  state: 'active',
  provider_tenant_id: '111',
  provider_account_ref: '222',
  settings: {
    launcher: {
      trigger: 'pull_request_opened',
      scope_kind: 'repository',
      scope_ref: '123',
      slots: [{ key: 'default', agent_profile_id: fakeId('aprf') }],
    },
  },
})

let root: Root, container: HTMLDivElement, restore: () => void
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
  window.history.replaceState(null, '', '/')
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function render(api: ReturnType<typeof fakeApi>, content: ReactNode) {
  return renderProjectApp(root, api, content)
}

function RemoveApp({ onRemoved }: { onRemoved: () => void }) {
  const actions = useProjectAppActions(orgId, projectId)
  return <RemoveProjectAppButton app={savedApp} actions={actions} onRemoved={onRemoved} />
}

async function selectAction(name: string) {
  act(() => {
    button('App actions').dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }),
    )
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="menuitem"]')).not.toBeNull()
  })
  const item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
    (element) => element.textContent.trim() === name,
  )
  if (!item) throw new Error(`Missing app action: ${name}`)
  act(() => {
    item.click()
  })
}

it('links every catalog entry by its exact app type', async () => {
  const definitions = [
    appDefinition('slack_thread'),
    appDefinition('discord_thread'),
    appDefinition('github_pr'),
  ]
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/app-definitions`,
      respond: () => Response.json({ data: definitions }),
    },
  ])
  render(api, <AppCatalog orgId={orgId} projectId={projectId} />)
  await waitForUI(() => {
    expect(container.querySelectorAll('a')).toHaveLength(definitions.length)
  })
  expect([...container.querySelectorAll('a')].map((link) => link.getAttribute('href'))).toEqual(
    definitions.map((definition) => `/projects/${projectId}/apps/new/${definition.app_type}`),
  )
  expect([...container.querySelectorAll('h2')].map((heading) => heading.textContent)).toEqual([
    'Slack threads',
    'Discord threads',
    'GitHub PR review',
  ])
})

it('lists apps without a profile, retries a failed page and retains previously loaded apps', async () => {
  let secondAttempts = 0
  const secondApp = { ...savedApp, id: `app_${'b'.repeat(26)}`, name: 'second-setup' }
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/apps`,
      respond: ({ url }) => {
        if (!url.searchParams.has('cursor'))
          return jsonResponse(z.json().parse({ data: [savedApp], next_cursor: 'page-two' }))
        secondAttempts++
        if (secondAttempts === 1)
          return jsonResponse({ code: 'internal_error', error: 'Temporary failure' }, 500)
        return jsonResponse(z.json().parse({ data: [secondApp], next_cursor: null }))
      },
    },
  ])
  render(api, <ProjectAppsList orgId={orgId} projectId={projectId} canManage />)
  await waitForUI(() => {
    expect(document.body.textContent).toContain(savedApp.name)
  })
  expect(
    container.querySelector(`a[href="/projects/${projectId}/apps/${savedApp.id}"]`),
  ).not.toBeNull()
  expect(container.querySelector(`a[href="/projects/${projectId}/apps/new"]`)?.textContent).toBe(
    'Add app',
  )
  act(() => {
    button('Load more apps').click()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Could not load apps.')
  })
  act(() => {
    button('Retry').click()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain(savedApp.name)
  })
  act(() => {
    button('Load more apps').click()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain(secondApp.name)
  })
  expect(document.body.textContent).toContain(savedApp.name)
  expect(container.querySelectorAll('a')).toHaveLength(3)
  expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
})

it.each([true, false, undefined])(
  'offers the catalog for an empty project only to managers (canManage=%s)',
  async (canManage) => {
    const definitions = [
      appDefinition('slack_thread'),
      appDefinition('discord_thread'),
      appDefinition('github_pr'),
    ]
    const api = fakeApi([
      {
        method: 'GET',
        path: `${projectPath}/apps`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: `${projectPath}/app-definitions`,
        respond: () => Response.json({ data: definitions }),
      },
    ])
    render(api, <ProjectAppsList orgId={orgId} projectId={projectId} canManage={canManage} />)
    if (canManage) {
      await waitForUI(() => {
        expect(container.querySelectorAll('a')).toHaveLength(definitions.length)
      })
      expect([...container.querySelectorAll('a')].map((link) => link.getAttribute('href'))).toEqual(
        definitions.map((definition) => `/projects/${projectId}/apps/new/${definition.app_type}`),
      )
    } else {
      await waitForUI(() => {
        expect(container.textContent).toContain('Ask a project administrator to add one.')
      })
      expect(container.querySelectorAll('a')).toHaveLength(0)
      expect(api.requestsTo('GET', `${projectPath}/app-definitions`)).toHaveLength(0)
    }
  },
)

it.each([true, false])(
  'respects management access in app details (canManage=%s)',
  async (canManage) => {
    let app = savedApp
    let attempts = 0
    const api = fakeApi([
      {
        method: 'GET',
        path: `${projectPath}/apps/${app.id}/subscriptions`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: `${projectPath}/apps/${app.id}`,
        respond: () => jsonResponse(z.json().parse(app)),
      },
      {
        method: 'POST',
        path: `${projectPath}/apps/${app.id}/disconnect`,
        respond: () => {
          attempts++
          if (attempts === 1)
            return jsonResponse({ code: 'internal_error', error: 'Try again shortly' }, 500)
          app = {
            ...app,
            state: 'disconnected',
            setup_revision: app.setup_revision + 1,
            updated_at: '2026-09-19T00:01:00Z',
          }
          return jsonResponse(z.json().parse(app))
        },
      },
    ])
    render(
      api,
      <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage={canManage} />,
    )
    await waitForUI(() => {
      expect(container.querySelector('h1')?.textContent).toBe(app.name)
    })
    expect(container.querySelector('[aria-label="Pull requests"]')).not.toBeNull()
    expect(container.querySelector('[aria-label="Conversations"]')).not.toBeNull()
    if (!canManage) {
      expect(container.querySelector('[aria-label="App actions"]')).toBeNull()
      expect(container.querySelector('form')).toBeNull()
      expect(() => button('Edit')).toThrow('Missing button')
      expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
      return
    }
    await selectAction('Reconnect account')
    const tenant = container.querySelector<HTMLInputElement>('#provider-tenant')
    expect(tenant?.value).toBe(savedApp.provider_tenant_id)
    expect(tenant?.readOnly).toBe(true)
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    act(() => {
      button('Cancel').click()
    })
    expect(container.querySelector('form')).toBeNull()
    expect(button('Edit')).toBeDefined()
    const confirm = vi.fn(() => false)
    vi.stubGlobal('confirm', confirm)
    await selectAction('Disconnect app')
    expect(confirm).toHaveBeenCalledWith(
      expect.stringContaining('Subscriptions, agents and history are kept.'),
    )
    expect(api.requestsTo('POST', `${projectPath}/apps/${app.id}/disconnect`)).toHaveLength(0)
    confirm.mockReturnValue(true)
    await selectAction('Disconnect app')
    await waitForUI(() => {
      expect(document.body.textContent).toContain('Try again shortly')
    })
    expect(button('Delete app')).toBeDefined()
    await selectAction('Disconnect app')
    await waitForUI(() => {
      expect(container.textContent).toContain('This app is disconnected.')
    })
    expect(api.requestsTo('POST', `${projectPath}/apps/${app.id}/disconnect`)).toHaveLength(2)
    confirm.mockReturnValue(false)
    act(() => {
      button('Delete app').click()
    })
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('This deletes its schedules'))
    expect(api.requestsTo('DELETE', `${projectPath}/apps/${app.id}`)).toHaveLength(0)
  },
)

it.each(['slack_thread', 'discord_thread', 'github_pr'] as const)(
  'shows usable %s capability keys from the app',
  (appType) => {
    const app = projectApp({ app_type: appType, name: 'customer-support' })
    render(fakeApi([]), <ProjectAppAdvanced app={app} />)
    expect(button('Advanced').getAttribute('aria-expanded')).toBe('false')
    act(() => {
      button('Advanced').click()
    })
    const section = container.querySelector('[aria-label="Advanced"]')
    const keys = [...(section?.querySelectorAll('code') ?? [])].map((code) => code.textContent)
    expect(keys).toContain('app__customer-support__read')
    expect(section?.textContent).toContain('Subscription types')
    expect(keys).toContain(appType === 'github_pr' ? 'pull_request' : 'thread_messages')
    if (appType === 'github_pr') {
      expect(keys).not.toContain('interaction_handlers')
    } else {
      expect(keys).toContain('interaction_handlers')
      expect(keys).toContain('customer-support')
    }
  },
)

it.each(['active', 'disconnected'] as const)(
  'shows current connection failures only for an active app and refreshes status (%s)',
  async (state) => {
    let failing = true
    const app = projectApp({
      app_type: 'discord_thread',
      state,
      provider_tenant_id: '111',
      provider_account_ref: '222',
    })
    const api = fakeApi([
      {
        method: 'GET',
        path: `${projectPath}/apps/${app.id}/subscriptions`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: `${projectPath}/apps/${app.id}`,
        respond: () =>
          Response.json({
            ...app,
            runtime_failure: failing
              ? {
                  message: 'Discord Gateway closed: 4014',
                  retry_at: '2026-09-23T12:00:00Z',
                }
              : undefined,
          }),
      },
    ])
    render(
      api,
      <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage={false} />,
    )
    await waitForUI(() => {
      expect(container.querySelector('h1')?.textContent).toBe(app.name)
    })
    if (state === 'disconnected') {
      expect(container.textContent).not.toContain('Connection failed:')
      expect(container.textContent).toContain('not queued for replay')
      return
    }
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      'Discord Gateway closed: 4014',
    )
    expect(container.querySelector('time')?.dateTime).toBe('2026-09-23T12:00:00Z')
    failing = false
    act(() => {
      button('Refresh status').click()
    })
    await waitForUI(() => {
      expect(container.textContent).not.toContain('Connection failed:')
    })
    expect(api.requestsTo('GET', `${projectPath}/apps/${app.id}`)).toHaveLength(2)
  },
)

it.each([true, false])(
  'keeps runtime failures while saving until GET reports the current failure (still failing: %s)',
  async (stillFailing) => {
    const failure = {
      message: 'Discord Gateway closed: 4014',
      retry_at: '2026-09-23T12:00:00Z',
    }
    const app = projectApp({
      app_type: 'discord_thread',
      state: 'active',
      provider_tenant_id: '111',
      provider_account_ref: '222',
    })
    const appPath = `${projectPath}/apps/${app.id}`
    const updated = { ...app, updated_at: '2026-09-23T11:00:00Z' }
    let saved = false
    let release!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => {
      release = resolve
    })
    const api = fakeApi([
      {
        method: 'GET',
        path: appPath,
        respond: () => (saved ? pending : Response.json({ ...app, runtime_failure: failure })),
      },
      {
        method: 'PUT',
        path: appPath,
        respond: () => {
          saved = true
          return Response.json(updated)
        },
      },
      ...['agent-profiles', 'cron-triggers', `apps/${app.id}/subscriptions`].map((resource) => ({
        method: 'GET',
        path: `${projectPath}/${resource}`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      })),
    ])
    const { cache, client } = render(
      api,
      <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage />,
    )
    await waitForUI(() => {
      expect(container.textContent).toContain(`Connection failed: ${failure.message}`)
    })
    act(() => {
      button('Choose profiles').click()
    })
    act(() => {
      button('Save changes').click()
    })
    await waitForUI(() => {
      expect(api.requestsTo('PUT', appPath)).toHaveLength(1)
      expect(api.requestsTo('GET', appPath)).toHaveLength(2)
    })
    const queryKey = getProjectAppQueryKey({
      path: { orgID: orgId, projectID: projectId, appID: app.id },
      client,
    })
    expect(cache.getQueryData(queryKey)).toMatchObject({ runtime_failure: failure })
    expect(container.textContent).toContain(`Connection failed: ${failure.message}`)
    act(() => {
      release(Response.json({ ...updated, runtime_failure: stillFailing ? failure : undefined }))
    })
    await waitForUI(() => {
      expect(button('Choose profiles')).toBeDefined()
      expect(cache.getQueryData(queryKey)).toEqual({
        ...updated,
        runtime_failure: stillFailing ? failure : undefined,
      })
      expect(container.textContent.includes(`Connection failed: ${failure.message}`)).toBe(
        stillFailing,
      )
    })
  },
)

it('keeps an edit draft mounted through a failed background refresh', async () => {
  let unavailable = false
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/apps/${savedApp.id}/subscriptions`,
      respond: () => Response.json({ data: [], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `${projectPath}/apps/${savedApp.id}`,
      respond: () =>
        unavailable
          ? jsonResponse({ code: 'internal_error', error: 'Temporary failure' }, 500)
          : jsonResponse(z.json().parse(savedApp)),
    },
  ])
  const { cache } = render(
    api,
    <ProjectAppDetail orgId={orgId} projectId={projectId} appId={savedApp.id} canManage />,
  )
  await waitForUI(() => {
    expect(button('Edit')).toBeDefined()
  })
  act(() => {
    button('Edit').click()
  })
  act(() => {
    const select = container.querySelector<HTMLSelectElement>('#app-trigger')
    if (!select) throw new Error('Missing trigger selector')
    select.value = 'mention'
    select.dispatchEvent(new Event('change', { bubbles: true }))
  })
  expect(container.querySelector('#launcher-scope')).toBeNull()
  expect(container.textContent).toContain('Restricted to repository 123')
  const draft = container.querySelector('form')
  unavailable = true
  await act(async () => {
    await cache.invalidateQueries()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain(
      'Could not refresh this app. Your current edits are kept.',
    )
  })
  expect(container.querySelector<HTMLSelectElement>('#app-trigger')?.value).toBe('mention')
  expect(container.querySelector('form')).toBe(draft)
  unavailable = false
  act(() => {
    button('Retry refresh').click()
  })
  await waitForUI(() => {
    expect(document.body.textContent).not.toContain('Could not refresh this app.')
  })
  expect(container.querySelector<HTMLSelectElement>('#app-trigger')?.value).toBe('mention')
})

it('does not let a delayed GET overwrite a successful app update', async () => {
  let app = savedApp
  let delay = false
  let release!: (response: Response) => void
  const pending = new Promise<Response>((resolve) => {
    release = resolve
  })
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/apps/${app.id}/subscriptions`,
      respond: () => Response.json({ data: [], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `${projectPath}/apps/${app.id}`,
      respond: () => (delay ? pending : jsonResponse(z.json().parse(app))),
    },
    {
      method: 'POST',
      path: `${projectPath}/apps/${app.id}/disconnect`,
      respond: () => {
        app = {
          ...app,
          state: 'disconnected',
          setup_revision: app.setup_revision + 1,
          updated_at: '2026-09-19T00:01:00Z',
        }
        return jsonResponse(z.json().parse(app))
      },
    },
  ])
  const { cache, client } = render(
    api,
    <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage />,
  )
  await waitForUI(() => {
    expect(button('Delete app')).toBeDefined()
  })
  vi.stubGlobal('confirm', () => true)
  delay = true
  let refresh!: Promise<void>
  act(() => {
    refresh = cache.invalidateQueries()
  })
  await waitForUI(() => {
    expect(api.requestsTo('GET', `${projectPath}/apps/${app.id}`)).toHaveLength(2)
  })
  await selectAction('Disconnect app')
  await waitForUI(() => {
    expect(container.textContent).toContain('This app is disconnected.')
  })
  await act(async () => {
    release(jsonResponse(z.json().parse(savedApp)))
    await refresh
  })
  const queryKey = getProjectAppQueryKey({
    path: { orgID: orgId, projectID: projectId, appID: app.id },
    client,
  })
  expect(cache.getQueryData(queryKey)).toMatchObject({ state: 'disconnected' })
  expect(button('Reconnect account')).toBeDefined()
})

it('waits for disconnect to settle before removal and keeps the app cache deleted', async () => {
  const detailPath = `${projectPath}/apps/${savedApp.id}`
  let release!: (response: Response) => void
  const pending = new Promise<Response>((resolve) => {
    release = resolve
  })
  const api = fakeApi([
    { method: 'GET', path: detailPath, respond: () => Response.json(savedApp) },
    {
      method: 'GET',
      path: `${detailPath}/subscriptions`,
      respond: () => Response.json({ data: [], next_cursor: null }),
    },
    { method: 'POST', path: `${detailPath}/disconnect`, respond: () => pending },
    { method: 'DELETE', path: detailPath, respond: () => new Response(null, { status: 204 }) },
  ])
  const { cache, client, rerender } = render(
    api,
    <ProjectAppDetail orgId={orgId} projectId={projectId} appId={savedApp.id} canManage />,
  )
  const queryKey = getProjectAppQueryKey({
    path: { orgID: orgId, projectID: projectId, appID: savedApp.id },
    client,
  })
  await waitForUI(() => {
    expect(button('Delete app').disabled).toBe(false)
  })
  vi.stubGlobal('confirm', () => true)
  await selectAction('Disconnect app')
  await waitForUI(() => {
    expect(api.requestsTo('POST', `${detailPath}/disconnect`)).toHaveLength(1)
    expect(button('Delete app').disabled).toBe(true)
    expect(button('App actions').disabled).toBe(true)
  })
  await act(async () => {
    button('Delete app').click()
    await Promise.resolve()
  })
  expect(api.requestsTo('DELETE', detailPath)).toHaveLength(0)
  await act(async () => {
    release(
      Response.json({
        ...savedApp,
        state: 'disconnected',
        setup_revision: savedApp.setup_revision + 1,
      }),
    )
    await pending
  })
  await waitForUI(() => {
    expect(button('Delete app').disabled).toBe(false)
    expect(cache.getQueryData(queryKey)).toMatchObject({ state: 'disconnected' })
  })
  const removed = vi.fn(() => {
    rerender(null)
  })
  rerender(<RemoveApp onRemoved={removed} />)
  act(() => {
    button('Delete app').click()
  })
  await waitForUI(() => {
    expect(removed).toHaveBeenCalledOnce()
    expect(api.requestsTo('DELETE', detailPath)).toHaveLength(1)
    expect(cache.isMutating()).toBe(0)
    expect(cache.getQueryState(queryKey)).toBeUndefined()
  })
})

it('removes deleted app details and does not restore them from a delayed read', async () => {
  let deleted = false
  let release!: (response: Response) => void
  const pending = new Promise<Response>((resolve) => {
    release = resolve
  })
  let reads = 0
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/apps/${savedApp.id}/subscriptions`,
      respond: () => Response.json({ data: [], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `${projectPath}/apps/${savedApp.id}`,
      respond: () => {
        reads++
        if (deleted) return jsonResponse({ code: 'not_found', error: 'App removed' }, 404)
        return reads === 1 ? jsonResponse(z.json().parse(savedApp)) : pending
      },
    },
    {
      method: 'DELETE',
      path: `${projectPath}/apps/${savedApp.id}`,
      respond: () => {
        deleted = true
        return new Response(null, { status: 204 })
      },
    },
  ])
  const detail = (
    <ProjectAppDetail orgId={orgId} projectId={projectId} appId={savedApp.id} canManage />
  )
  const { cache, client, rerender } = render(api, detail)
  await waitForUI(() => {
    expect(button('Delete app')).toBeDefined()
  })
  let refresh!: Promise<void>
  act(() => {
    refresh = cache.invalidateQueries()
  })
  await waitForUI(() => {
    expect(reads).toBe(2)
  })
  rerender(
    <RemoveApp
      onRemoved={() => {
        rerender(null)
      }}
    />,
  )
  vi.stubGlobal('confirm', () => true)
  act(() => {
    button('Delete app').click()
  })
  const queryKey = getProjectAppQueryKey({
    path: { orgID: orgId, projectID: projectId, appID: savedApp.id },
    client,
  })
  await waitForUI(() => {
    expect(deleted).toBe(true)
    expect(cache.getQueryState(queryKey)).toBeUndefined()
  })
  await act(async () => {
    release(jsonResponse(z.json().parse(savedApp)))
    await refresh
  })
  expect(cache.getQueryState(queryKey)).toBeUndefined()
  rerender(detail)
  await waitForUI(() => {
    expect(container.textContent).toContain('Could not load this app.')
  })
  expect(reads).toBe(3)
  expect(container.querySelector('[aria-label="Pull requests"]')).toBeNull()
  expect(container.textContent).not.toContain('Delete app')
})

it.each([401, 403, 404])(
  'stops showing app settings after a %s refresh response',
  async (status) => {
    let unavailable = false
    const api = fakeApi([
      {
        method: 'GET',
        path: `${projectPath}/apps/${savedApp.id}/subscriptions`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: `${projectPath}/apps/${savedApp.id}`,
        respond: () =>
          unavailable
            ? jsonResponse({ code: 'unavailable', error: 'Unavailable' }, status)
            : jsonResponse(z.json().parse(savedApp)),
      },
    ])
    const { cache } = render(
      api,
      <ProjectAppDetail orgId={orgId} projectId={projectId} appId={savedApp.id} canManage />,
    )
    await waitForUI(() => {
      expect(button('Edit')).toBeDefined()
    })
    unavailable = true
    await act(async () => {
      await cache.invalidateQueries()
    })
    await waitForUI(() => {
      expect(container.textContent).toContain('Could not load this app.')
    })
    expect(container.querySelector('[aria-label="Pull requests"]')).toBeNull()
  },
)

it.each([
  ['slack_thread', 'Use an existing Slack app'],
  ['github_pr', 'GitHub App owner'],
  ['discord_thread', 'Bot token'],
] as const)('creates a %s app through its connection form', async (appType, control) => {
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/app-definitions`,
      respond: () => Response.json({ data: [appDefinition(appType)] }),
    },
  ])
  render(api, <ProjectAppCreateSetup orgId={orgId} projectId={projectId} appType={appType} />)
  await waitForUI(() => {
    expect(container.querySelector('[aria-label="Connection"]')?.textContent).toContain(control)
  })
  expect(container.querySelector('#app-name')).not.toBeNull()
  expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
})

it('keeps creation fields mounted when refreshing app definitions fails', async () => {
  let failing = false
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/app-definitions`,
      respond: () =>
        failing
          ? jsonResponse({ code: 'internal_error', error: 'Temporary failure' }, 500)
          : Response.json({ data: [appDefinition('discord_thread')] }),
    },
  ])
  const { cache, client } = render(
    api,
    <ProjectAppCreateSetup orgId={orgId} projectId={projectId} appType="discord_thread" />,
  )
  await waitForUI(() => {
    expect(container.querySelector('#bot-token')).not.toBeNull()
  })
  await enter('App name', 'engineering')
  await enter('Bot token', 'unsaved-token')
  failing = true
  await act(async () => {
    await cache.invalidateQueries({
      queryKey: listAppDefinitionsQueryKey({
        path: { orgID: orgId, projectID: projectId },
        client,
      }),
    })
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Your setup is kept')
  })
  expect(container.querySelector<HTMLInputElement>('#app-name')?.value).toBe('engineering')
  expect(container.querySelector<HTMLInputElement>('#bot-token')?.value).toBe('unsaved-token')
  failing = false
  act(() => {
    button('Retry refresh').click()
  })
  await waitForUI(() => {
    expect(container.textContent).not.toContain('Your setup is kept')
  })
  expect(container.querySelector<HTMLInputElement>('#bot-token')?.value).toBe('unsaved-token')
})
