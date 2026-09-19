/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type ProjectApp, schemas } from '@omnara/sdk'
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
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, waitForUI } from '@/test/secret-editor'

import { ProjectAppActions } from './ProjectAppActions'
import { ProjectAppsList } from './ProjectAppsList'

const orgId = fakeId('org'),
  projectId = fakeId('proj')
const projectPath = `/api/v1/orgs/${orgId}/projects/${projectId}`
const savedApp: ProjectApp = {
  id: fakeId('app'),
  project_id: projectId,
  name: 'Read-only PR tools',
  enabled: true,
  settings: { resource: { definition: 'omnara.github', tools: { github_read: {} } } },
  created_at: '2026-09-19T00:00:00Z',
  updated_at: '2026-09-19T00:00:00Z',
}
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
  const secondApp = { ...savedApp, id: `app_${'b'.repeat(26)}`, name: 'Second setup' }
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
        path: `${projectPath}/apps/${app.id}`,
        respond: () => jsonResponse(z.json().parse(app)),
      },
      {
        method: 'PUT',
        path: `${projectPath}/apps/${app.id}`,
        respond: ({ body }) => {
          attempts++
          if (attempts === 1)
            return jsonResponse({ code: 'internal_error', error: 'Try again shortly' }, 500)
          app = {
            ...app,
            ...schemas.zSaveProjectAppRequest.parse(body),
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
    act(() => {
      button('Disable app').click()
    })
    await waitForUI(() => {
      expect(document.body.textContent).toContain('Try again shortly')
    })
    expect(button('Disable app')).toBeDefined()
    act(() => {
      button('Disable app').click()
    })
    await waitForUI(() => {
      expect(button('Enable app')).toBeDefined()
    })
    expect(api.requestsTo('PUT', `${projectPath}/apps/${app.id}`).at(-1)?.body).toEqual({
      name: savedApp.name,
      enabled: false,
      settings: savedApp.settings,
    })
    const confirm = vi.fn(() => false)
    vi.stubGlobal('confirm', confirm)
    act(() => {
      button('Remove app').click()
    })
    expect(confirm).toHaveBeenCalled()
    expect(api.requestsTo('DELETE', `${projectPath}/apps/${app.id}`)).toHaveLength(0)
  },
)

it('keeps an edit draft mounted through a failed background refresh', async () => {
  let unavailable = false
  const api = fakeApi([
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
  await enter('App setup name', 'Unsaved draft')
  unavailable = true
  await act(async () => {
    await cache.invalidateQueries()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain(
      'Could not refresh this app. Your current edits are kept.',
    )
  })
  expect(container.querySelector<HTMLInputElement>('#app-name')?.value).toBe('Unsaved draft')
  unavailable = false
  act(() => {
    button('Retry refresh').click()
  })
  await waitForUI(() => {
    expect(document.body.textContent).not.toContain('Could not refresh this app.')
  })
  expect(container.querySelector<HTMLInputElement>('#app-name')?.value).toBe('Unsaved draft')
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
      path: `${projectPath}/apps/${app.id}`,
      respond: () => (delay ? pending : jsonResponse(z.json().parse(app))),
    },
    {
      method: 'PUT',
      path: `${projectPath}/apps/${app.id}`,
      respond: ({ body }) => {
        app = {
          ...app,
          ...schemas.zSaveProjectAppRequest.parse(body),
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
    expect(button('Disable app')).toBeDefined()
  })
  delay = true
  let refresh!: Promise<void>
  act(() => {
    refresh = cache.invalidateQueries()
  })
  await waitForUI(() => {
    expect(api.requestsTo('GET', `${projectPath}/apps/${app.id}`)).toHaveLength(2)
  })
  act(() => {
    button('Disable app').click()
  })
  await waitForUI(() => {
    expect(button('Enable app')).toBeDefined()
  })
  await act(async () => {
    release(jsonResponse(z.json().parse(savedApp)))
    await refresh
  })
  const queryKey = getProjectAppQueryKey({
    path: { orgID: orgId, projectID: projectId, appID: app.id },
    client,
  })
  expect(cache.getQueryData(queryKey)).toMatchObject({ enabled: false })
  expect(button('Enable app')).toBeDefined()
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
