/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { type ConfiguredModel, createOmnaraClient, type ModelProviderConfig } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode, Suspense, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'

import { Dialog, DialogContent } from '@/components/ui/dialog'
import { type FakeApi, fakeApi, type FakeRoute, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

import { AddDiscoveredModelsStep } from './AddDiscoveredModelsStep'
import { CreateConfiguredModelDialog } from './CreateConfiguredModelDialog'
import { GrantConfiguredModelDialog } from './GrantConfiguredModelDialog'

const timestamp = '2026-01-01T00:00:00Z'
const orgId = fakeId('org')
const provider = {
  id: fakeId('mpc'),
  org_id: orgId,
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
  created_at: timestamp,
  updated_at: timestamp,
} satisfies ModelProviderConfig
const model = {
  id: fakeId('mdl'),
  org_id: orgId,
  model_provider_config_id: provider.id,
  management_kind: 'tenant',
  name: 'model-one',
  current_revision_id: fakeId('mrev'),
  provider_model_slug: 'model-one',
  context_window_tokens: 100000,
  max_output_tokens: null,
  supports_tools: true,
  supports_reasoning: false,
  default_reasoning_effort: '',
  supported_reasoning_efforts: [],
  input_modalities: [],
  output_modalities: [],
  api_variant_options: {},
  created_at: timestamp,
  updated_at: timestamp,
  revision_created_at: timestamp,
} satisfies ConfiguredModel
const discoveredModels = ['model-one', 'model-two'].map((slug) => ({
  slug,
  context_window_tokens: 100000,
}))
const projects = ['Alpha', 'Beta', 'Gamma'].map((name) => ({
  id: `proj_${name.charAt(0).toLowerCase().repeat(26)}`,
  org_id: orgId,
  name,
  access: { can_read: true, can_manage: true, can_manage_access: true, can_operate: true },
  created_at: timestamp,
  updated_at: timestamp,
}))
const modelsPath = `/api/v1/orgs/${orgId}/model-provider-configs/${provider.id}/models`
const grantPath = (projectId: string) => `/api/v1/orgs/${orgId}/projects/${projectId}/model-grants`

let container: HTMLDivElement
let root: Root
let queryClient: QueryClient
let restoreActEnvironment: () => void

beforeAll(() => {
  restoreActEnvironment = enableReactActEnvironment()
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

function creationApi(createModel: FakeRoute['respond'] = () => jsonResponse(model, 201)) {
  const grantFailures = new Map([
    ['Beta', 1],
    ['Gamma', 2],
  ])
  return fakeApi([
    { method: 'POST', path: modelsPath, respond: createModel },
    {
      method: 'GET',
      path: `/api/v1/orgs/${orgId}/projects`,
      respond: () => jsonResponse({ data: projects, next_cursor: null }),
    },
    {
      method: 'GET',
      path: `/api/v1/orgs/${orgId}/model-provider-configs/${provider.id}/model-catalog`,
      respond: () => jsonResponse({ status: 'ok', models: discoveredModels }),
    },
    ...projects.map((project) => ({
      method: 'POST',
      path: grantPath(project.id),
      respond: () => {
        const failuresRemaining = grantFailures.get(project.name) ?? 0
        if (failuresRemaining > 0) {
          grantFailures.set(project.name, failuresRemaining - 1)
          return jsonResponse({ code: 'internal', error: 'Grant temporarily unavailable' }, 503)
        }
        return jsonResponse(
          {
            grant: {
              id: fakeId('pmog'),
              org_id: orgId,
              project_id: project.id,
              configured_model_id: model.id,
              supported_reasoning_efforts: [],
              input_modalities: [],
              output_modalities: [],
              created_at: timestamp,
              updated_at: timestamp,
            },
          },
          201,
        )
      },
    })),
  ])
}

async function interact(action: () => void) {
  await act(async () => {
    action()
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

async function render(api: FakeApi, content: ReactNode) {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  await interact(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <Suspense fallback={null}>{content}</Suspense>
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
}

function element(selector: string): HTMLElement {
  const match = document.querySelector(selector)
  if (!(match instanceof HTMLElement)) throw new Error(`Missing test element: ${selector}`)
  return match
}

function button(label: string): HTMLButtonElement {
  const match = [...document.querySelectorAll('button')].find(
    (candidate) => candidate.textContent === label,
  )
  if (!match) throw new Error(`Missing button: ${label}`)
  return match
}

async function clickButton(label: string) {
  await interact(() => {
    button(label).click()
  })
}

async function openCombobox(selector: string) {
  await interact(() => {
    element(selector).dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }),
    )
  })
}

async function selectOption(selector: string, label: string) {
  await openCombobox(selector)
  await interact(() => {
    const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(
      (candidate) => candidate.textContent === label,
    )
    if (!option) throw new Error(`Missing option: ${label}`)
    option.click()
  })
}

function ConfiguredModelDialog({ create = true }: { create?: boolean }) {
  const [open, setOpen] = useState(true)
  return (
    <>
      <button
        onClick={() => {
          setOpen(true)
        }}
      >
        Open
      </button>
      {create ? (
        <CreateConfiguredModelDialog
          open={open}
          onOpenChange={setOpen}
          orgId={orgId}
          providers={[provider]}
        />
      ) : (
        open && (
          <GrantConfiguredModelDialog open onOpenChange={setOpen} orgId={orgId} model={model} />
        )
      )}
    </>
  )
}

async function selectModelAndProjects(create = true) {
  if (create) await selectOption('#cm-provider-model-slug', 'model-one')
  for (const project of projects) {
    await selectOption('[aria-label="Search projects…"]', project.name)
  }
}

it('excludes successful bulk creations and retries only the failed selection', async () => {
  let attempt = 0
  const api = creationApi(() => {
    attempt += 1
    return attempt === 2
      ? jsonResponse({ code: 'internal', error: 'Model temporarily unavailable' }, 503)
      : jsonResponse(model, 201)
  })
  const onDone = vi.fn()
  await render(
    api,
    <Dialog open>
      <DialogContent>
        <AddDiscoveredModelsStep
          orgId={orgId}
          provider={provider}
          discoveredModels={discoveredModels}
          onDone={onDone}
        />
      </DialogContent>
    </Dialog>,
  )
  for (const discovered of discoveredModels) {
    await selectOption('[aria-label="Search detected models…"]', discovered.slug)
  }
  await clickButton('Create 2 models')

  expect(document.body.textContent).toContain('Created 1 of 2 models.')
  expect(button('Done')).toBeDefined()
  await openCombobox('[aria-label="Search detected models…"]')
  expect(
    [...document.querySelectorAll('[role="option"]')].map((option) => option.textContent),
  ).toEqual(['model-two'])
  await clickButton('Create 1 model')

  expect(api.requestsTo('POST', modelsPath).map((request) => request.body)).toMatchObject([
    { name: 'model-one' },
    { name: 'model-two' },
    { name: 'model-two' },
  ])
  expect(onDone).toHaveBeenCalledOnce()
})

it.each([
  { create: true, submitLabel: 'Add model', retryLabel: 'Retry project grants' },
  { create: false, submitLabel: 'Grant model', retryLabel: 'Grant model' },
])(
  '$submitLabel retains successful grants across partial failures',
  async ({ create, submitLabel, retryLabel }) => {
    const api = creationApi()
    await render(api, <ConfiguredModelDialog create={create} />)
    await selectModelAndProjects(create)
    await clickButton(submitLabel)

    expect(document.body.textContent).toContain('2 project grants failed')
    for (const remaining of [['Beta', 'Gamma'], ['Gamma']]) {
      await openCombobox('[aria-label="Search projects…"]')
      expect(
        [...document.querySelectorAll('[role="option"]')].map((option) => option.textContent),
      ).toEqual(remaining)
      await clickButton(retryLabel)
    }

    expect(api.requestsTo('POST', modelsPath)).toHaveLength(create ? 1 : 0)
    expect(projects.map((project) => api.requestsTo('POST', grantPath(project.id)).length)).toEqual(
      [1, 2, 3],
    )
    for (const request of api.requests.filter((request) =>
      request.url.pathname.endsWith('/model-grants'),
    )) {
      expect(request.body).toEqual({ configured_model_id: model.id })
    }
    expect(document.querySelector('[role="dialog"]')).toBeNull()
  },
)

it('keeps unsubmitted drafts but clears a created model when abandoning failed grants', async () => {
  const api = creationApi()
  await render(api, <ConfiguredModelDialog />)
  await selectModelAndProjects()
  await clickButton('Close')
  await clickButton('Open')
  expect(element('#cm-name')).toHaveProperty('value', 'model-one')
  expect(element('[aria-label="Remove Alpha"]')).toBeDefined()
  await clickButton('Add model')
  expect(button('Retry project grants')).toBeDefined()

  await clickButton('Close')
  await clickButton('Open')

  expect(element('#cm-name')).toHaveProperty('value', '')
  expect(element('#cm-provider-model-slug')).toHaveProperty('value', '')
  expect(document.querySelector('[aria-label="Remove Beta"]')).toBeNull()
  expect(button('Add model').disabled).toBe(true)
  expect(api.requestsTo('POST', modelsPath)).toHaveLength(1)
  expect(api.requests.filter((request) => request.method === 'DELETE')).toEqual([])
})
