/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type CurrentUserOrg, type UsageReport } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { OrganizationNav } from '@/components/app-shell/OrganizationNav'
import { SidebarProvider } from '@/components/ui/sidebar'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { OrganizationUsagePage } from '@/routes/OrganizationUsagePage'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

const orgId = fakeId('org')
const usagePath = `/api/v1/orgs/${orgId}/usage`
const report: UsageReport = {
  totals: {
    model_calls: 0,
    tokens: {
      input_tokens_total: 0,
      uncached_input_tokens: 0,
      cache_read_input_tokens: 0,
      cache_write_input_tokens: 0,
      output_tokens_total: 0,
      reasoning_output_tokens: 0,
    },
    cost: { provider_reported_usd: '0', model_calls_with_reported_cost: 0 },
  },
  by_model: [],
}
let root: Root
let container: HTMLDivElement
let queryClient: QueryClient
let restore: () => void

beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
  queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  queryClient.clear()
  container.remove()
  vi.unstubAllGlobals()
  restore()
})

async function render(role: CurrentUserOrg['role']) {
  const api = fakeApi([
    { method: 'GET', path: '/api/web-config', respond: () => jsonResponse({}) },
    { method: 'GET', path: usagePath, respond: () => jsonResponse(report) },
  ])
  vi.stubGlobal('fetch', api.fetch)
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  const rootRoute = createRootRoute()
  const usageRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/usage',
    component: () => (
      <SidebarProvider>
        <OrganizationNav />
        <OrganizationUsagePage />
      </SidebarProvider>
    ),
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([usageRoute]),
    history: createMemoryHistory({ initialEntries: ['/usage'] }),
  })
  await router.load()
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ActiveOrgContext
            value={{
              orgs: [],
              activeOrg: { id: orgId, name: 'Test', role, created_at: '2026-09-15T00:00:00Z' },
              setActiveOrgId: () => undefined,
            }}
          >
            <RouterProvider router={router} />
          </ActiveOrgContext>
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await Promise.resolve()
  })
  return api
}

it('hides org Usage and Apps and denies a direct Usage visit without an admin request', async () => {
  const api = await render('member')
  await waitForUI(() => {
    expect(container.querySelector('h1')?.textContent).toBe('Not allowed')
  })
  expect(container.querySelector('a[href="/usage"]')).toBeNull()
  expect(container.querySelector('a[href="/apps"]')).toBeNull()
  expect(container.querySelector('a[href="/models"]')).not.toBeNull()
  expect(container.textContent).toContain('You don’t have permission to view usage')
  expect(api.requestsTo('GET', usagePath)).toHaveLength(0)
})

it.each(['owner', 'admin'] as const)(
  'keeps org Usage and Apps available to an %s',
  async (role) => {
    const api = await render(role)
    await waitForUI(() => {
      expect(container.textContent).toContain('No model usage recorded yet.')
    })
    expect(container.querySelector('a[href="/usage"]')?.textContent).toBe('Usage')
    expect(container.querySelector('a[href="/apps"]')?.textContent).toBe('Apps')
    expect(container.querySelector('h1')?.textContent).toBe('Usage')
    expect(api.requestsTo('GET', usagePath)).toHaveLength(1)
  },
)
