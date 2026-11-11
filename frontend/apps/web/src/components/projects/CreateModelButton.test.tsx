/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type ModelProviderConfig } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act, createContext, type ReactNode, useContext } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it } from 'vitest'

import { ActiveOrgContext } from '@/lib/active-org-context'
import { type FakeApi, fakeApi, jsonResponse, type RecordedRequest } from '@/test/fake-api'
import { currentUserOrg, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

import { CreateModelButton } from './CreateModelButton'

const timestamp = '2026-01-01T00:00:00Z'
const activeOrg = currentUserOrg({ id: fakeId('org') })
const projectId = fakeId('proj')
const providersPath = `/api/v1/orgs/${activeOrg.id}/model-provider-configs`
const provider = {
  id: fakeId('mpc'),
  org_id: activeOrg.id,
  management_kind: 'tenant',
  name: 'Provider',
  api_format: 'openai-responses',
  api_variant: 'openai',
  base_url: 'https://provider.example.com',
  endpoint_path: '/responses',
  request_timeout_ms: 120000,
  idle_timeout_ms: 300000,
  auth_kind: 'bearer_token',
  auth_options: {},
  credential_secret_id: fakeId('sec'),
  headers: {},
  secret_headers: {},
  created_at: timestamp,
  updated_at: timestamp,
} satisfies ModelProviderConfig

const TestContentContext = createContext<ReactNode>(null)
function TestContent() {
  return useContext(TestContentContext)
}
const rootRoute = createRootRoute()
const testRouter = createRouter({
  routeTree: rootRoute.addChildren([
    createRoute({
      getParentRoute: () => rootRoute,
      path: '/projects/$projectId',
      component: TestContent,
    }),
  ]),
  history: createMemoryHistory({ initialEntries: [`/projects/${projectId}`] }),
})

let container: HTMLDivElement
let root: Root
let queryClient: QueryClient
let restoreActEnvironment: () => void

beforeAll(async () => {
  restoreActEnvironment = enableReactActEnvironment()
  await testRouter.load()
})
afterAll(() => {
  restoreActEnvironment()
})
beforeEach(() => {
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
})

function buttonApi(providers: (request: RecordedRequest) => Response) {
  return fakeApi([
    { method: 'GET', path: providersPath, respond: providers },
    {
      method: 'GET',
      path: `/api/v1/orgs/${activeOrg.id}/projects`,
      respond: () =>
        jsonResponse({
          data: [
            {
              id: projectId,
              org_id: activeOrg.id,
              name: 'Alpha',
              access: {
                can_read: true,
                can_manage: true,
                can_manage_access: true,
                can_operate: true,
              },
              created_at: timestamp,
              updated_at: timestamp,
            },
          ],
          next_cursor: null,
        }),
    },
  ])
}

async function render(api: FakeApi) {
  const client = createOmnaraClient({
    baseUrl: 'https://omnara.test/api/v1',
    fetch: api.fetch,
    maxRetries: 0,
  })
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ActiveOrgContext.Provider
            value={{ orgs: [activeOrg], activeOrg, setActiveOrgId: () => undefined }}
          >
            <TestContentContext value={<CreateModelButton />}>
              <RouterProvider router={testRouter} />
            </TestContentContext>
          </ActiveOrgContext.Provider>
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

function newModelButton() {
  const match = [...container.querySelectorAll('button')].find(
    (candidate) => candidate.textContent === 'New model',
  )
  if (!match) throw new Error('Missing New model button')
  return match
}

it('keeps the provider wizard open after the first provider is created', async () => {
  let providers: ModelProviderConfig[] = []
  const api = buttonApi(() => jsonResponse({ data: providers, next_cursor: null }))
  await render(api)
  await waitForUI(() => {
    expect(newModelButton().disabled).toBe(false)
  })

  await act(async () => {
    newModelButton().click()
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  await waitForUI(() => {
    expect(document.querySelector('#mp-name')).not.toBeNull()
  })

  // Creating the provider refreshes the list the button reads.
  providers = [provider]
  await act(async () => {
    await queryClient.invalidateQueries()
  })
  await waitForUI(() => {
    expect(api.requestsTo('GET', providersPath).length).toBeGreaterThan(1)
  })
  expect(document.querySelector('#mp-name')).not.toBeNull()
})

it('offers providers from every page', async () => {
  const secondProvider = { ...provider, id: `mpc_${'b'.repeat(26)}`, name: 'Second provider' }
  const api = buttonApi((request) =>
    request.url.searchParams.get('cursor') === 'page-2'
      ? jsonResponse({ data: [secondProvider], next_cursor: null })
      : jsonResponse({ data: [provider], next_cursor: 'page-2' }),
  )
  await render(api)
  await waitForUI(() => {
    expect(newModelButton().disabled).toBe(false)
  })
  expect(api.requestsTo('GET', providersPath)).toHaveLength(2)

  await act(async () => {
    newModelButton().click()
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  // The dialog offers a provider choice only when it has more than one provider.
  await waitForUI(() => {
    expect(document.querySelector('[aria-label="Provider"]')).not.toBeNull()
  })
})

it('does not start the provider wizard when the provider list fails to load', async () => {
  const api = buttonApi(() => jsonResponse({ code: 'internal', error: 'unavailable' }, 503))
  await render(api)
  await waitForUI(() => {
    expect(newModelButton().title).toBe('Could not load model providers.')
  })
  expect(newModelButton().disabled).toBe(true)
})
