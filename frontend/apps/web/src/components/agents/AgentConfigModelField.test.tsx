/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { type ConfiguredModelSummary, createOmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act, createContext, type ReactNode, useContext } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'

import { AgentConfigModelField } from '@/components/agents/AgentConfigModelField'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { type FakeApi, fakeApi, jsonResponse, neverResponds } from '@/test/fake-api'
import { currentUserOrg, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { translateTextNodes } from '@/test/translate-text-nodes'

const TestContentContext = createContext<ReactNode>(null)
function TestContent() {
  return useContext(TestContentContext)
}
const testRouter = createRouter({
  routeTree: createRootRoute({ component: TestContent }),
  history: createMemoryHistory(),
})

const timestamp = '2026-01-01T00:00:00Z'
const activeOrg = currentUserOrg({ id: fakeId('org'), name: 'Test org' })
const projectId = fakeId('proj')
const providerId = fakeId('mpc')

function configuredModel(id: string, name: string): ConfiguredModelSummary {
  return {
    id,
    org_id: activeOrg.id,
    model_provider_config_id: providerId,
    name,
    provider_config: 'omnara-openrouter',
    provider_model_slug: name,
    created_at: timestamp,
    updated_at: timestamp,
  }
}

const pricedModel = configuredModel(fakeId('mdl'), 'openai/gpt-5.6-sol')
const unpricedModel = configuredModel(`mdl_${'b'.repeat(26)}`, 'x-ai/grok-5')

function modelGrant(model: ConfiguredModelSummary) {
  return {
    grant: {
      id: model.id.replace('mdl_', 'pmog_'),
      org_id: activeOrg.id,
      project_id: projectId,
      configured_model_id: model.id,
      supported_reasoning_efforts: [],
      input_modalities: [],
      output_modalities: [],
      created_at: timestamp,
      updated_at: timestamp,
    },
    model,
  }
}

let container: HTMLDivElement
let root: Root
let api: FakeApi
let Providers: ({ children }: { children: ReactNode }) => ReactNode
let restoreActEnvironment: () => void

beforeAll(async () => {
  restoreActEnvironment = enableReactActEnvironment()
  await testRouter.load()
})

afterAll(() => {
  restoreActEnvironment()
})

beforeEach(() => {
  api = fakeApi([
    {
      method: 'GET',
      path: `/api/v1/orgs/${activeOrg.id}/projects/${projectId}/model-grants`,
      respond: (request) =>
        request.url.searchParams.has('name')
          ? neverResponds()
          : jsonResponse({ data: [pricedModel, unpricedModel].map(modelGrant), next_cursor: null }),
    },
    {
      method: 'GET',
      path: `/api/v1/orgs/${activeOrg.id}/model-provider-configs`,
      respond: () =>
        jsonResponse({
          data: [
            {
              id: providerId,
              org_id: activeOrg.id,
              management_kind: 'cluster',
              name: 'omnara-openrouter',
              api_format: 'openai-responses',
              api_variant: 'openai',
              base_url: 'https://openrouter.example.com',
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
            },
          ],
          next_cursor: null,
        }),
    },
    {
      method: 'GET',
      path: `/api/v1/orgs/${activeOrg.id}/model-provider-configs/${providerId}/model-catalog`,
      respond: () =>
        jsonResponse({
          status: 'ok',
          models: [
            {
              slug: pricedModel.provider_model_slug,
              pricing: { input_usd_per_million: '1.25', output_usd_per_million: '10' },
            },
          ],
        }),
    },
    {
      method: 'GET',
      path: `/api/v1/orgs/${activeOrg.id}/projects`,
      respond: () => jsonResponse({ data: [], next_cursor: null }),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  Providers = function Providers({ children }) {
    return (
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ActiveOrgContext.Provider
            value={{ orgs: [activeOrg], activeOrg, setActiveOrgId: () => undefined }}
          >
            <TestContentContext value={children}>
              <RouterProvider router={testRouter} />
            </TestContentContext>
          </ActiveOrgContext.Provider>
        </QueryClientProvider>
      </OmnaraClientProvider>
    )
  }
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
})

async function renderField(model: ConfiguredModelSummary) {
  await act(async () => {
    root.render(
      <Providers>
        <AgentConfigModelField
          orgId={activeOrg.id}
          projectId={projectId}
          value={{ providerConfig: model.provider_config, modelName: model.name }}
          onChange={() => undefined}
        />
      </Providers>,
    )
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

async function renderTranslatedPricedField() {
  await renderField(pricedModel)
  await vi.waitFor(() => {
    expect(container.textContent).toContain('$1.25 in · $10.00 out per 1M tokens')
  })
  translateTextNodes(container)
}

it('keeps searching after a browser translator rewrites the selected model pricing', async () => {
  await renderTranslatedPricedField()

  await act(async () => {
    container
      .querySelector('#agent-config-model')
      ?.dispatchEvent(new KeyboardEvent('keydown', { key: 'g', bubbles: true }))
    await new Promise((resolve) => setTimeout(resolve, 300))
  })

  expect(api.requests.some(({ url }) => url.searchParams.get('name') === '*g*')).toBe(true)
  expect(container.querySelector('#agent-config-model')).not.toBeNull()
})

it('switches to an unpriced model after a browser translator rewrites the pricing', async () => {
  await renderTranslatedPricedField()

  await renderField(unpricedModel)

  expect(container.querySelector('#agent-config-model')?.textContent).toBe(
    'x-ai/grok-5 · omnara-openrouter',
  )
  expect(container.textContent).toContain('— per 1M tokens')
})
