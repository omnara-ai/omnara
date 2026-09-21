/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import { getProjectAppQueryKey } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterContextProvider,
} from '@tanstack/react-router'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { z } from 'zod'

import { ProjectAppDetail } from '@/routes/ProjectAppPage'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId, projectApp } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, waitForUI } from '@/test/secret-editor'

import { ProjectAppActions } from './ProjectAppActions'
import { ProjectAppsList } from './ProjectAppsList'
import { ProjectAppSummary } from './ProjectAppSummary'

const orgId = fakeId('org'),
  projectId = fakeId('proj')
const projectPath = `/api/v1/orgs/${orgId}/projects/${projectId}`
const savedApp = projectApp({
  provider: 'github',
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
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function render(api: ReturnType<typeof fakeApi>, content: ReactNode) {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  const cache = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: 30_000 } },
  })
  const router = createRouter({ routeTree: createRootRoute(), history: createMemoryHistory() })
  function rerender(content: ReactNode) {
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>
            <RouterContextProvider router={router}>{content}</RouterContextProvider>
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
    })
  }
  rerender(content)
  return { cache, client, rerender }
}

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
  render(api, <ProjectAppsList orgId={orgId} projectId={projectId} />)
  await waitForUI(() => {
    expect(document.body.textContent).toContain(savedApp.name)
  })
  expect(container.querySelector('a')?.getAttribute('href')).toBe(
    `/projects/${projectId}/apps/${savedApp.id}`,
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
  expect(container.querySelectorAll('a')).toHaveLength(2)
  expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
})

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
    if (!canManage) {
      expect(container.querySelectorAll('button')).toHaveLength(0)
      expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
      return
    }
    vi.stubGlobal('confirm', () => true)
    act(() => {
      button('Disconnect app').click()
    })
    await waitForUI(() => {
      expect(document.body.textContent).toContain('Try again shortly')
    })
    expect(button('Disconnect app')).toBeDefined()
    act(() => {
      button('Disconnect app').click()
    })
    await waitForUI(() => {
      expect(button('Reconnect account')).toBeDefined()
    })
    expect(container.textContent).not.toContain('Finish setup:')
    expect(api.requestsTo('POST', `${projectPath}/apps/${app.id}/disconnect`)).toHaveLength(2)
    const confirm = vi.fn(() => false)
    vi.stubGlobal('confirm', confirm)
    act(() => {
      button('Remove app').click()
    })
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('This deletes its schedules'))
    expect(api.requestsTo('DELETE', `${projectPath}/apps/${app.id}`)).toHaveLength(0)
  },
)

it.each([true, false])(
  'explains the next step for a fresh draft (canManage=%s)',
  async (canManage) => {
    const app = projectApp()
    render(
      fakeApi([
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
          method: 'GET',
          path: `${projectPath}/cron-triggers`,
          respond: () => Response.json({ data: [], next_cursor: null }),
        },
      ]),
      <ProjectAppDetail orgId={orgId} projectId={projectId} appId={app.id} canManage={canManage} />,
    )
    await waitForUI(() => {
      expect(container.textContent).toContain(
        canManage
          ? 'Finish setup: connect an account'
          : 'Ask a project administrator to connect this app.',
      )
    })
    await waitForUI(() => {
      expect(container.textContent).toContain('No schedules yet.')
    })
    if (canManage) expect(button('Connect account')).toBeDefined()
    else expect(container.querySelectorAll('button')).toHaveLength(0)
  },
)

it.each(['slack', 'discord', 'github'] as const)(
  'shows usable %s capability keys from the app',
  (provider) => {
    const app = projectApp({ provider, name: 'customer-support' })
    render(fakeApi([]), <ProjectAppSummary orgId={orgId} projectId={projectId} app={app} />)
    const section = container.querySelector('[aria-label="Capabilities"]')
    const keys = [...(section?.querySelectorAll('code') ?? [])].map((code) => code.textContent)
    expect(keys).toContain('app__customer-support__read')
    expect(section?.textContent).toContain('Subscription types')
    expect(keys).toContain(provider === 'github' ? 'pull_request' : 'thread_messages')
    if (provider === 'github') {
      expect(keys).not.toContain('interaction_handlers')
      expect(keys).not.toContain('customer-support')
    } else {
      expect(keys).toContain('interaction_handlers')
      expect(keys).toContain('customer-support')
    }
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
    expect(button('Edit settings')).toBeDefined()
  })
  act(() => {
    button('Edit settings').click()
  })
  await enter('Repository ID', '999')
  unavailable = true
  await act(async () => {
    await cache.invalidateQueries()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain(
      'Could not refresh this app. Your current edits are kept.',
    )
  })
  expect(container.querySelector<HTMLInputElement>('#launcher-scope')?.value).toBe('999')
  unavailable = false
  act(() => {
    button('Retry refresh').click()
  })
  await waitForUI(() => {
    expect(document.body.textContent).not.toContain('Could not refresh this app.')
  })
  expect(container.querySelector<HTMLInputElement>('#launcher-scope')?.value).toBe('999')
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
    expect(button('Disconnect app')).toBeDefined()
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
  act(() => {
    button('Disconnect app').click()
  })
  await waitForUI(() => {
    expect(button('Reconnect account')).toBeDefined()
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
    expect(button('Remove app')).toBeDefined()
  })
  let refresh!: Promise<void>
  act(() => {
    refresh = cache.invalidateQueries()
  })
  await waitForUI(() => {
    expect(reads).toBe(2)
  })
  rerender(
    <ProjectAppActions
      orgId={orgId}
      projectId={projectId}
      app={savedApp}
      onRemoved={() => {
        rerender(null)
      }}
    />,
  )
  vi.stubGlobal('confirm', () => true)
  act(() => {
    button('Remove app').click()
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
  expect(container.textContent).not.toContain('Edit settings')
  expect(container.textContent).not.toContain('Remove app')
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
      expect(button('Edit settings')).toBeDefined()
    })
    unavailable = true
    await act(async () => {
      await cache.invalidateQueries()
    })
    await waitForUI(() => {
      expect(container.textContent).toContain('Could not load this app.')
    })
    expect(container.textContent).not.toContain('Edit settings')
  },
)
