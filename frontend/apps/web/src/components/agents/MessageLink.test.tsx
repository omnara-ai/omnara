/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it } from 'vitest'

import { AgentChatMessage } from '@/components/agents/AgentChatMessage'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { currentUserOrg } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

it('opens a link to a dashboard page in place and every other link in a new tab', async () => {
  const restore = enableReactActEnvironment()
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  const client = createOmnaraClient({
    baseUrl: 'https://omnara.test/api/v1',
    fetch: () => Promise.reject(new Error('unexpected request')),
  })
  const cache = new QueryClient()
  cache.setQueryData(['web-config'], {})
  const org = currentUserOrg({ id: 'org_test' })
  const rootRoute = createRootRoute({ component: Outlet })
  const chat = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: () => (
      <AgentChatMessage
        orgID="org_test"
        projectID="proj_test"
        agentID="agt_test"
        message={{
          id: 'reply',
          role: 'assistant',
          parts: [
            {
              id: 'reply-text',
              type: 'text',
              text: 'See the [Usage report](/usage?range=30d), [a file](/src/app.ts) or [the docs](https://docs.omnara.com).',
            },
          ],
        }}
      />
    ),
  })
  const usage = createRoute({
    getParentRoute: () => rootRoute,
    path: '/usage',
    component: () => <p>Usage page</p>,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([chat, usage]),
    history: createMemoryHistory(),
  })
  function link(name: string) {
    const match = [...container.querySelectorAll('a')].find((item) => item.textContent === name)
    if (match == null) throw new Error(`Missing link ${name}`)
    return match
  }
  try {
    await act(async () => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>
            <ActiveOrgContext
              value={{ activeOrg: org, orgs: [org], setActiveOrgId: () => undefined }}
            >
              <RouterProvider router={router} />
            </ActiveOrgContext>
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
      await router.load()
    })
    await waitForUI(() => {
      expect(link('Usage report').getAttribute('href')).toBe('/usage?range=30d')
    })
    expect(link('Usage report').hasAttribute('target')).toBe(false)
    expect(link('a file').getAttribute('target')).toBe('_blank')
    expect(link('the docs').getAttribute('target')).toBe('_blank')

    act(() => {
      link('Usage report').click()
    })
    await waitForUI(() => {
      expect(container.textContent).toContain('Usage page')
    })
    expect(router.state.location.href).toBe('/usage?range=30d')
  } finally {
    act(() => {
      root.unmount()
    })
    cache.clear()
    container.remove()
    restore()
  }
})
