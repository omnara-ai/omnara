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
import { jsonResponse } from '@/test/fake-api'
import { currentUser, currentUserOrg, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

import { ProjectSwitcher } from './ProjectSwitcher'

const org = currentUserOrg({ id: fakeId('org'), name: 'Acme' })
const timestamp = '2026-01-01T00:00:00Z'

function project(id: string, name: string) {
  return {
    id,
    org_id: org.id,
    name,
    access: { can_read: true, can_manage: true, can_manage_access: true, can_operate: true },
    created_at: timestamp,
    updated_at: timestamp,
  }
}

const alpha = project(`proj_${'a'.repeat(26)}`, 'Alpha')
const beta = project(`proj_${'b'.repeat(26)}`, 'Beta')

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

it('names the current project even when it is not on the first page', async () => {
  const client = createOmnaraClient({
    baseUrl: 'https://omnara.test/api/v1',
    fetch: (input, init) => {
      const url = new URL(new Request(input, init).url)
      return Promise.resolve(
        url.searchParams.get('cursor') === 'page-2'
          ? jsonResponse({ data: [beta], next_cursor: null })
          : jsonResponse({ data: [alpha], next_cursor: 'page-2' }),
      )
    },
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  queryClient.setQueryData(getCurrentUserQueryKey({ client }), currentUser([org]))
  const rootRoute = createRootRoute()
  const shell = () => (
    <ActiveOrgProvider>
      <ProjectSwitcher />
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
    ]),
    history: createMemoryHistory({ initialEntries: [`/projects/${beta.id}`] }),
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

  await waitForUI(() => {
    expect(container.querySelector('button')?.textContent).toBe('Beta')
  })
})

it.each([
  { source: 'agents?tab=instances', destination: 'agents', search: { tab: 'instances' } },
  { source: 'integrations', destination: 'integrations', search: {} },
  { source: 'integrations/itg_example', destination: 'integrations', search: {} },
  { source: 'integrations/new', destination: 'integrations', search: {} },
  { source: 'integrations/new/slack_thread', destination: 'integrations', search: {} },
])(
  'keeps the section when switching projects from $source',
  async ({ source, destination, search }) => {
    const client = createOmnaraClient({
      baseUrl: 'https://omnara.test/api/v1',
      fetch: () => Promise.resolve(jsonResponse({ data: [alpha, beta], next_cursor: null })),
    })
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    queryClient.setQueryData(getCurrentUserQueryKey({ client }), currentUser([org]))
    const rootRoute = createRootRoute()
    const shell = () => (
      <ActiveOrgProvider>
        <ProjectSwitcher />
      </ActiveOrgProvider>
    )
    const router = createRouter({
      routeTree: rootRoute.addChildren(
        [
          '/',
          '/integrations',
          '/projects/$projectId/agents',
          '/projects/$projectId/integrations',
          '/projects/$projectId/integrations/$integrationId',
          '/projects/$projectId/integrations/new',
          '/projects/$projectId/integrations/new/$integrationKind',
        ].map((path) => createRoute({ getParentRoute: () => rootRoute, path, component: shell })),
      ),
      history: createMemoryHistory({
        initialEntries: [`/projects/${alpha.id}/${source}`],
      }),
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
    await waitForUI(() => {
      expect(container.querySelector('button')?.textContent).toBe('Alpha')
    })

    await act(async () => {
      container
        .querySelector('button')
        ?.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, button: 0 }))
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    await act(async () => {
      ;[...document.querySelectorAll<HTMLElement>('[role="menuitem"]')]
        .find((item) => item.textContent === 'Beta')
        ?.click()
      await new Promise((resolve) => setTimeout(resolve, 0))
    })

    await waitForUI(() => {
      expect(router.state.location.pathname).toBe(`/projects/${beta.id}/${destination}`)
    })
    expect(router.state.location.search).toEqual(search)

    if (destination === 'integrations') {
      await act(async () => {
        container
          .querySelector('button')
          ?.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, button: 0 }))
        await new Promise((resolve) => setTimeout(resolve, 0))
      })
      await act(async () => {
        ;[...document.querySelectorAll<HTMLElement>('[role="menuitem"]')]
          .find((item) => item.textContent === 'All projects')
          ?.click()
        await new Promise((resolve) => setTimeout(resolve, 0))
      })
      // All projects lands on the org-level picker that asks which project to open.
      await waitForUI(() => {
        expect(router.state.location.pathname).toBe('/integrations')
      })
    }
  },
)
