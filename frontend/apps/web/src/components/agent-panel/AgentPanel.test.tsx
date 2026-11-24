/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { type AgentPanel as Panel, createOmnaraClient, schemas } from '@omnara/sdk'
import { getAgentPanelQueryKey, getCurrentUserQueryKey } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { act, Suspense, useContext } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'

import { fakeApi, jsonResponse, neverResponds } from '@/test/fake-api'
import { currentUser, currentUserOrg, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

import { AgentPanel } from './AgentPanel'
import { AssistantContext } from './assistant-context'

function Home() {
  const { setDock } = useContext(AssistantContext)
  return <section ref={setDock} aria-label="Home" />
}

async function mountPanel({
  actorsUnavailable = false,
  panelRefresh,
  queued = false,
}: { actorsUnavailable?: boolean; panelRefresh?: Promise<void>; queued?: boolean } = {}) {
  const restore = enableReactActEnvironment()
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  const org = currentUserOrg()
  const projectID = fakeId('proj')
  const agentID = fakeId('agt')
  const projectPath = `/api/v1/orgs/${org.id}/projects/${projectID}`
  const agentPath = `${projectPath}/agents/${agentID}`
  const historyPath = `${agentPath}/events`
  const historyRequests: Request[] = []
  const scope = {
    org_id: org.id,
    org_name: org.name,
    project_id: projectID,
    project_name: 'Default',
  }
  const connected: Panel = {
    revision: 1,
    state: 'ready',
    selection: { agent_id: agentID, name: 'Omnara assistant', can_operate: true, scope },
  }
  let panel: Panel = queued
    ? {
        revision: 0,
        state: 'empty',
        setup: { request_id: crypto.randomUUID(), model: 'available', scope },
      }
    : connected
  const createPath = `/api/v1/orgs/${org.id}/assistant-agents`
  const api = fakeApi([
    {
      method: 'POST',
      path: createPath,
      respond: () => {
        panel = connected
        return jsonResponse({ agent_id: agentID, panel })
      },
    },
    ...[agentPath, historyPath, `${agentPath}/inputs`, `${agentPath}/interactions`].map((path) => ({
      method: 'GET',
      path,
      respond: neverResponds,
    })),
    {
      method: 'GET',
      path: `${projectPath}/actors`,
      respond: () =>
        actorsUnavailable
          ? jsonResponse({ code: 'forbidden', message: 'Project access lost' }, 403)
          : jsonResponse({ data: [], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `/api/v1/orgs/${org.id}/projects`,
      respond: () => jsonResponse({ data: [], next_cursor: null }),
    },
    {
      method: 'GET',
      path: '/api/v1/me/agent-panel',
      respond: async () => {
        await panelRefresh
        return jsonResponse(panel)
      },
    },
    {
      method: 'PATCH',
      path: '/api/v1/me/agent-panel',
      respond: ({ body }) => {
        schemas.zUpdateAgentPanelRequest.parse(body)
        panel = { revision: panel.revision + 1, state: 'empty' }
        return jsonResponse(panel)
      },
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({
    fetch: (input, init) => {
      const request = new Request(input, init)
      if (new URL(request.url).pathname === historyPath) historyRequests.push(request)
      return api.fetch(request)
    },
  })
  const cache = new QueryClient({
    defaultOptions: { queries: { staleTime: Infinity, retry: false } },
  })
  cache.setQueryData(getAgentPanelQueryKey({ client }), panel)
  cache.setQueryData(getCurrentUserQueryKey({ client }), currentUser([org]))
  const rootRoute = createRootRoute({
    component: () => (
      <AgentPanel>
        <Outlet />
      </AgentPanel>
    ),
  })
  const page = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: () => null,
  })
  const home = createRoute({ getParentRoute: () => rootRoute, path: '/home', component: Home })
  const router = createRouter({
    routeTree: rootRoute.addChildren([page, home]),
    history: createMemoryHistory(),
  })
  function button(label: string) {
    const match = [...container.querySelectorAll('button')].find(
      (item) => item.textContent.trim() === label || item.getAttribute('aria-label') === label,
    )
    if (match == null) throw new Error(`Missing ${label}`)
    return match
  }
  function cleanup() {
    act(() => {
      root.unmount()
    })
    cache.clear()
    container.remove()
    restore()
  }
  try {
    await act(async () => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>
            <Suspense fallback={null}>
              <RouterProvider router={router} />
            </Suspense>
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
      await router.load()
    })
    return { container, api, createPath, historyRequests, button, cleanup, router }
  } catch (error) {
    cleanup()
    throw error
  }
}

it('loads chat only after opening and aborts its pending history when disconnected', async () => {
  const { api, historyRequests, button, cleanup } = await mountPanel()
  try {
    expect(api.requests).toHaveLength(0)
    act(() => {
      button('Assistant').click()
    })
    await act(() => vi.dynamicImportSettled())
    await waitForUI(() => {
      expect(historyRequests).toHaveLength(1)
    })
    expect(historyRequests[0]?.signal.aborted).toBe(false)
    act(() => {
      button('Assistant settings').click()
    })
    await act(() => vi.dynamicImportSettled())
    await waitForUI(() => {
      expect(button('Disconnect')).toBeDefined()
    })
    act(() => {
      button('Disconnect').click()
    })
    await waitForUI(() => {
      expect(historyRequests[0]?.signal.aborted).toBe(true)
    })
  } finally {
    cleanup()
  }
})

it('creates the queued assistant while collapsed, then opens its chat', async () => {
  const { api, createPath, historyRequests, button, cleanup } = await mountPanel({ queued: true })
  try {
    await waitForUI(() => {
      expect(button('Assistant').getAttribute('aria-expanded')).toBe('true')
    })
    await act(() => vi.dynamicImportSettled())
    await waitForUI(() => {
      expect(historyRequests).toHaveLength(1)
    })
    expect(api.requestsTo('POST', createPath).map(({ body }) => body)).toMatchObject([
      { automatic: true },
    ])
  } finally {
    cleanup()
  }
})

it('moves the conversation into a page that offers it a home and back to the corner without restarting it', async () => {
  const { container, historyRequests, button, cleanup, router } = await mountPanel()
  try {
    await act(async () => {
      router.history.push('/home')
      await router.load()
    })
    await act(() => vi.dynamicImportSettled())
    const dock = container.querySelector('section[aria-label="Home"]')
    await waitForUI(() => {
      expect(dock?.querySelector('[data-assistant-surface]')).not.toBeNull()
      expect(historyRequests).toHaveLength(1)
    })
    const conversation = dock?.querySelector('[data-assistant-surface]')
    expect(container.querySelector('[data-floating-launcher]')).toBeNull()
    expect(dock?.querySelector('[aria-label="Close assistant"]')).toBeNull()

    await act(async () => {
      router.history.push('/')
      await router.load()
    })
    await waitForUI(() => {
      expect(button('Assistant').getAttribute('aria-expanded')).toBe('false')
    })
    expect(container.querySelector('#agent-panel')?.firstElementChild).toBe(conversation)
    expect(historyRequests).toHaveLength(1)
  } finally {
    cleanup()
  }
})

it('keeps the agent picker accessible when the selected conversation fails to load', async () => {
  const { container, button, cleanup } = await mountPanel({ actorsUnavailable: true })
  try {
    act(() => {
      button('Assistant').click()
    })
    await act(() => vi.dynamicImportSettled())
    await waitForUI(() => {
      expect(container.textContent).toContain('Could not load your assistant.')
    })
    act(() => {
      button('Assistant settings').click()
    })
    await act(() => vi.dynamicImportSettled())
    await waitForUI(() => {
      expect(button('Connect existing').getAttribute('aria-pressed')).toBe('true')
      expect(container.querySelector('#panel-project')).not.toBeNull()
    })
    expect(button('Disconnect')).toBeDefined()
  } finally {
    cleanup()
  }
})
