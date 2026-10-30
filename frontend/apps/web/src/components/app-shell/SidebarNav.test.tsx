/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import { getCurrentUserQueryKey } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act, Suspense } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { ActiveOrgProvider } from '@/components/active-org/ActiveOrgProvider'
import { SidebarProvider } from '@/components/ui/sidebar'
import { webConfigQuery } from '@/lib/web-config'
import { jsonResponse } from '@/test/fake-api'
import { currentUser, currentUserOrg, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

import { SidebarNav } from './SidebarNav'

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
})

async function renderAt(path: string, role: 'owner' | 'member') {
  const client = createOmnaraClient({
    baseUrl: 'https://omnara.test/api/v1',
    fetch: () => Promise.resolve(jsonResponse({ data: [], next_cursor: null })),
  })
  const org = currentUserOrg({ id: fakeId('org'), role })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  queryClient.setQueryData(getCurrentUserQueryKey({ client }), currentUser([org]))
  queryClient.setQueryData(webConfigQuery.queryKey, { billingURL: 'https://billing.test' })
  const rootRoute = createRootRoute()
  const shell = () => (
    <ActiveOrgProvider>
      <SidebarProvider>
        <SidebarNav />
      </SidebarProvider>
    </ActiveOrgProvider>
  )
  const router = createRouter({
    routeTree: rootRoute.addChildren([
      createRoute({ getParentRoute: () => rootRoute, path: '/', component: shell }),
      createRoute({
        getParentRoute: () => rootRoute,
        path: '/projects/$projectId',
        component: shell,
      }),
      createRoute({
        getParentRoute: () => rootRoute,
        path: '/projects/$projectId/models',
        component: shell,
      }),
    ]),
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <Suspense fallback={null}>
            <RouterProvider router={router} />
          </Suspense>
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

const projectPath = `/projects/proj_${'a'.repeat(26)}`

function links() {
  return [...container.querySelectorAll('a')].map((link) => ({
    label: link.textContent,
    href: link.getAttribute('href'),
  }))
}

function labels() {
  return links().map((link) => link.label)
}

const adminLabels = [
  'Overview',
  'Agents',
  'Integrations',
  'Usage',
  'Credits',
  'Models',
  'Machines',
  'Secrets',
  'Skills',
  'Memory',
  'Members',
  'API Tokens',
]

it.each(['/', projectPath])('keeps every item in place at %s', async (path) => {
  await renderAt(path, 'owner')
  await waitForUI(() => {
    expect(labels()).toEqual(adminLabels)
  })
})

it('points scoped items at the project and org-wide items at the organization', async () => {
  await renderAt(`${projectPath}/models`, 'owner')
  await waitForUI(() => {
    expect(links()).toEqual(
      expect.arrayContaining([
        { label: 'Overview', href: projectPath },
        { label: 'Models', href: `${projectPath}/models` },
        { label: 'Members', href: '/members' },
        { label: 'API Tokens', href: '/api-tokens' },
      ]),
    )
  })
  const active = container.querySelector('[data-active="true"]')
  expect(active?.textContent).toBe('Models')
})

it('hides API tokens and credits from members', async () => {
  await renderAt(projectPath, 'member')
  await waitForUI(() => {
    expect(labels()).toEqual(
      adminLabels.filter((label) => label !== 'API Tokens' && label !== 'Credits'),
    )
  })
})
