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
import { act, Suspense } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'

import { fakeApi, jsonResponse, neverResponds } from '@/test/fake-api'
import { currentUser, currentUserOrg, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

import { AgentPanel } from './AgentPanel'
import { AgentPanelSetting } from './AgentPanelSetting'

async function mountPanel({
  actorsUnavailable = false,
  panelRefresh,
}: { actorsUnavailable?: boolean; panelRefresh?: Promise<void> } = {}) {
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
  let panel: Panel = {
    enabled: true,
    revision: 1,
    state: 'ready',
    selection: {
      agent_id: agentID,
      name: 'Omnara assistant',
      can_operate: true,
      scope: { org_id: org.id, org_name: org.name, project_id: projectID, project_name: 'Default' },
    },
  }
  const api = fakeApi([
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
        const update = schemas.zUpdateAgentPanelRequest.parse(body)
        panel = { ...panel, enabled: update.enabled ?? panel.enabled, revision: panel.revision + 1 }
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
    component: AgentPanelSetting,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([page]),
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
    return { container, api, historyRequests, button, cleanup }
  } catch (error) {
    cleanup()
    throw error
  }
}

it('loads chat only after opening and aborts its pending history when turned off', async () => {
  const { container, api, historyRequests, button, cleanup } = await mountPanel()
  try {
    expect(button('Turn off assistant panel')).toBeDefined()
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
      button('Turn off assistant panel').click()
    })
    await waitForUI(() => {
      expect(historyRequests[0]?.signal.aborted).toBe(true)
    })
    expect(container.querySelector('#agent-panel')?.hasAttribute('open')).toBe(false)
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
      expect(container.querySelector('#panel-organization')).not.toBeNull()
    })
    expect(button('Disconnect')).toBeDefined()
  } finally {
    cleanup()
  }
})

it('stays collapsed after turning off in panel settings and re-enabling from Account', async () => {
  let finishRefresh: () => void = () => undefined
  const panelRefresh = new Promise<void>((resolve) => {
    finishRefresh = resolve
  })
  const { container, api, button, cleanup } = await mountPanel({ panelRefresh })
  try {
    act(() => {
      button('Assistant').click()
      button('Assistant settings').click()
    })
    await act(() => vi.dynamicImportSettled())
    await waitForUI(() => {
      expect(button('Turn off panel')).toBeDefined()
    })
    act(() => {
      button('Turn off panel').click()
    })
    await waitForUI(() => {
      expect(button('Turn on assistant panel')).toBeDefined()
      expect(container.textContent).not.toContain('Turn off panel')
      expect(api.requestsTo('GET', '/api/v1/me/agent-panel')).toHaveLength(1)
    })
    // The PATCH response unmounts setup before its invalidation request finishes.
    await act(async () => {
      finishRefresh()
      await panelRefresh
    })
    act(() => {
      button('Turn on assistant panel').click()
    })
    await waitForUI(() => {
      expect(button('Turn off assistant panel')).toBeDefined()
      expect(button('Assistant').getAttribute('aria-expanded')).toBe('false')
      expect(container.querySelector('#agent-panel')?.hasAttribute('open')).toBe(false)
    })
  } finally {
    finishRefresh()
    cleanup()
  }
})
