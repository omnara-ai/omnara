/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { type ConfiguredModel, createOmnaraClient, type ModelProviderConfig } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode, Suspense, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'

import { Dialog, DialogContent } from '@/components/ui/dialog'
import {
  type FakeApi,
  fakeApi,
  type FakeRoute,
  jsonResponse,
  type JsonValue,
  neverResponds,
} from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

import { AddConfiguredModelsView } from './AddConfiguredModelsView'
import { CreateConfiguredModelDialog } from './CreateConfiguredModelDialog'
import { CreateModelProviderDialog } from './CreateModelProviderDialog'
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
  headers: {},
  secret_headers: {},
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
const discoveredModels: JsonValue[] = [
  { slug: 'model-one', context_window_tokens: 100000, max_output_tokens: 32000 },
  { slug: 'model-two', context_window_tokens: 200000 },
  { slug: 'model-three' },
  { slug: 'model-zero', context_window_tokens: 100000 },
]
const existingModel = {
  ...model,
  id: fakeId('mdl'),
  name: 'zero',
  provider_model_slug: 'model-zero',
}
const projects = ['Alpha', 'Beta', 'Gamma'].map((name) => ({
  id: `proj_${name.charAt(0).toLowerCase().repeat(26)}`,
  org_id: orgId,
  name,
  access: { can_read: true, can_manage: true, can_manage_access: true, can_operate: true },
  created_at: timestamp,
  updated_at: timestamp,
}))
const modelsPath = `/api/v1/orgs/${orgId}/model-provider-configs/${provider.id}/models`
const catalogPath = `/api/v1/orgs/${orgId}/model-provider-configs/${provider.id}/model-catalog`
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

function creationApi({
  createModel = () => jsonResponse(model, 201),
  catalog = { status: 'ok', models: discoveredModels },
  failingProjects = new Map([
    ['Beta', 1],
    ['Gamma', 2],
  ]),
  extraRoutes = [],
}: {
  createModel?: FakeRoute['respond']
  catalog?: JsonValue
  failingProjects?: Map<string, number>
  extraRoutes?: FakeRoute[]
} = {}) {
  return fakeApi([
    ...extraRoutes,
    { method: 'POST', path: modelsPath, respond: createModel },
    {
      method: 'GET',
      path: modelsPath,
      respond: () => jsonResponse({ data: [existingModel], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `/api/v1/orgs/${orgId}/projects`,
      respond: () => jsonResponse({ data: projects, next_cursor: null }),
    },
    { method: 'GET', path: catalogPath, respond: () => jsonResponse(catalog) },
    ...projects.map((project) => ({
      method: 'POST',
      path: grantPath(project.id),
      respond: () => {
        const failuresRemaining = failingProjects.get(project.name) ?? 0
        if (failuresRemaining > 0) {
          failingProjects.set(project.name, failuresRemaining - 1)
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
    (candidate) => (candidate.getAttribute('aria-label') ?? candidate.textContent) === label,
  )
  if (!match) throw new Error(`Missing button: ${label}`)
  return match
}

async function clickButton(label: string) {
  await interact(() => {
    button(label).click()
  })
}

async function type(selector: string, value: string) {
  await interact(() => {
    const input = element(selector)
    if (!(input instanceof HTMLInputElement)) throw new Error(`Not an input: ${selector}`)
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function toggleModel(slug: string) {
  await interact(() => {
    const row = [...document.querySelectorAll('[aria-label="Models"] > li')].find(
      (candidate) => candidate.querySelector('.font-mono')?.textContent === slug,
    )
    const checkbox = row?.querySelector('input[type="checkbox"]')
    if (!(checkbox instanceof HTMLInputElement)) throw new Error(`Missing model row: ${slug}`)
    checkbox.click()
  })
}

function modelRows() {
  return [...document.querySelectorAll('[aria-label="Models"] > li')].map((row) =>
    row.textContent.trim(),
  )
}

async function selectOption(selector: string, label: string) {
  await interact(() => {
    element(selector).dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }),
    )
  })
  await interact(() => {
    const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(
      (candidate) => candidate.textContent === label,
    )
    if (!option) throw new Error(`Missing option: ${label}`)
    option.click()
  })
}

async function addProject(name: string) {
  await interact(() => {
    button('Project').dispatchEvent(
      new PointerEvent('pointerdown', { bubbles: true, button: 0, ctrlKey: false }),
    )
  })
  await interact(() => {
    const item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
      (candidate) => candidate.textContent === name,
    )
    if (!item) throw new Error(`Missing project: ${name}`)
    item.click()
  })
}

function AddModelsDialog() {
  const [open, setOpen] = useState(true)
  return (
    <CreateConfiguredModelDialog
      open={open}
      onOpenChange={setOpen}
      orgId={orgId}
      providers={[provider]}
    />
  )
}

it('lists catalog models, marks configured ones added, and creates the edited selection', async () => {
  const api = creationApi({ failingProjects: new Map() })
  await render(api, <AddModelsDialog />)

  await waitForUI(() => {
    expect(modelRows()).toEqual([
      'model-one100K',
      'model-two200K',
      'model-threeNo limits',
      'model-zeroAdded',
    ])
  })
  await type('[aria-label="Search models"]', 'TWO')
  expect(modelRows()).toEqual(['model-two200K', 'Add TWO as a custom model'])
  await type('[aria-label="Search models"]', '')

  await toggleModel('model-one')
  await toggleModel('model-three')
  expect(document.querySelector('#cm-draft-1-context')).not.toBeNull()
  expect(button('Add 2 models').disabled).toBe(true)
  await type('#cm-draft-1-context', '50000')
  await clickButton('Done editing model-three')
  await clickButton('Edit model-one')
  await type('#cm-draft-0-name', 'one')
  await addProject('Alpha')
  await clickButton('Add 2 models')

  expect(api.requestsTo('POST', modelsPath).map((request) => request.body)).toEqual([
    {
      name: 'one',
      provider_model_slug: 'model-one',
      context_window_tokens: 100000,
      max_output_tokens: 32000,
      supports_tools: true,
      supports_reasoning: false,
    },
    {
      name: 'model-three',
      provider_model_slug: 'model-three',
      context_window_tokens: 50000,
      supports_tools: true,
      supports_reasoning: false,
    },
  ])
  expect(api.requestsTo('POST', grantPath(projects[0]?.id ?? ''))).toHaveLength(2)
  expect(document.querySelector('[role="dialog"]')).toBeNull()
})

async function press(key: string, init: KeyboardEventInit = {}) {
  await interact(() => {
    const target = document.activeElement ?? document.body
    target.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, ...init }))
  })
}

function focusedLabel() {
  return document.activeElement?.getAttribute('aria-label')
}

function focusedRow() {
  return document.activeElement?.closest('label')?.querySelector('.font-mono')?.textContent
}

it('picks, edits, and adds models from the keyboard alone', async () => {
  const api = creationApi({ failingProjects: new Map() })
  await render(api, <AddModelsDialog />)
  await waitForUI(() => {
    expect(modelRows()).toContain('model-one100K')
  })
  expect(focusedLabel()).toBe('Search models')

  await press('ArrowDown')
  expect(focusedRow()).toBe('model-one')
  await press('Enter')
  expect(focusedRow()).toBe('model-one')
  await press('ArrowDown')
  expect(focusedRow()).toBe('model-two')
  await press('Enter')
  expect(modelRows().slice(0, 2)).toEqual(['model-oneEdit', 'model-twoEdit'])
  expect(focusedRow()).toBe('model-two')

  await press('ArrowRight')
  expect(document.activeElement?.id).toBe('cm-draft-1-name')
  await type('#cm-draft-1-name', 'two')
  await press('Enter')
  expect(document.querySelector('#cm-draft-1-name')).toBeNull()
  expect(focusedRow()).toBe('model-two')

  await press('ArrowUp')
  await press('ArrowUp')
  expect(focusedLabel()).toBe('Search models')
  await press('End')
  await press('ArrowDown')
  await press('m')
  expect(focusedLabel()).toBe('Search models')
  expect(element('[aria-label="Search models"]')).toHaveProperty('value', 'm')

  await press('Enter', { ctrlKey: true })
  expect(api.requestsTo('POST', modelsPath).map((request) => request.body)).toMatchObject([
    { name: 'model-one', provider_model_slug: 'model-one' },
    { name: 'two', provider_model_slug: 'model-two' },
  ])
  expect(document.querySelector('[role="dialog"]')).toBeNull()
})

it('shows one loading state until the models arrive', async () => {
  await render(
    creationApi({
      extraRoutes: [{ method: 'GET', path: catalogPath, respond: () => neverResponds() }],
    }),
    <AddModelsDialog />,
  )
  expect(modelRows()).toEqual(['Loading models from Provider…'])
})

it('says so when the provider reports no models', async () => {
  await render(
    creationApi({
      catalog: { status: 'ok', models: [] },
      extraRoutes: [
        {
          method: 'GET',
          path: modelsPath,
          respond: () => jsonResponse({ data: [], next_cursor: null }),
        },
      ],
    }),
    <AddModelsDialog />,
  )
  await waitForUI(() => {
    expect(modelRows()).toEqual([
      'This provider didn’t report any models. Enter a model slug above to add one.',
    ])
  })
})

it('keeps only failed models selected and retries only failed shares', async () => {
  let attempt = 0
  const api = creationApi({
    createModel: () => {
      attempt += 1
      return attempt === 2
        ? jsonResponse({ code: 'internal', error: 'Model temporarily unavailable' }, 503)
        : jsonResponse(model, 201)
    },
  })
  await render(api, <AddModelsDialog />)
  await waitForUI(() => {
    expect(modelRows()).toContain('model-one100K')
  })
  await toggleModel('model-one')
  await toggleModel('model-two')
  await addProject('Beta')
  await clickButton('Add 2 models')

  expect(document.body.textContent).toContain('Added 1 of 2 models.')
  expect(document.body.textContent).toContain('Sharing failed for 1 project')
  await clickButton('Retry sharing')
  expect(api.requestsTo('POST', grantPath(projects[1]?.id ?? ''))).toHaveLength(2)
  await clickButton('Add 1 model')

  expect(api.requestsTo('POST', modelsPath).map((request) => request.body)).toMatchObject([
    { name: 'model-one' },
    { name: 'model-two' },
    { name: 'model-two' },
  ])
  expect(document.querySelector('[role="dialog"]')).toBeNull()
})

it('warns when the catalog fails and adds a model by slug', async () => {
  const api = creationApi({ catalog: { status: 'failed', error: 'unauthorized' } })
  const onDone = vi.fn()
  await render(
    api,
    <Dialog open>
      <DialogContent>
        <AddConfiguredModelsView
          orgId={orgId}
          providers={[provider]}
          defaultProviderId={provider.id}
          initialCatalog={{ status: 'failed', error: 'unauthorized' }}
          dismissLabel="Skip for now"
          onDone={onDone}
        />
      </DialogContent>
    </Dialog>,
  )

  expect(element('[role="alert"]').textContent).toContain('unable to fetch available models')
  expect(api.requestsTo('GET', catalogPath)).toEqual([])
  await type('[aria-label="Search models"]', 'vendor/custom-model')
  await interact(() => {
    element('[aria-label="Search models"]').dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }),
    )
  })
  await type('#cm-draft-0-context', '64000')
  await clickButton('Add 1 model')

  expect(api.requestsTo('POST', modelsPath).map((request) => request.body)).toEqual([
    {
      name: 'vendor/custom-model',
      provider_model_slug: 'vendor/custom-model',
      context_window_tokens: 64000,
      supports_tools: true,
      supports_reasoning: false,
    },
  ])
  expect(onDone).toHaveBeenCalledOnce()
})

it('Share model retains successful grants across partial failures', async () => {
  const api = creationApi()
  function GrantDialog() {
    const [open, setOpen] = useState(true)
    return (
      open && <GrantConfiguredModelDialog open onOpenChange={setOpen} orgId={orgId} model={model} />
    )
  }
  await render(api, <GrantDialog />)
  for (const project of projects) {
    await selectOption('[aria-label="Search projects…"]', project.name)
  }
  await clickButton('Share model')

  expect(document.body.textContent).toContain('Sharing with 2 projects failed')
  for (const remaining of [['Beta', 'Gamma'], ['Gamma']]) {
    await interact(() => {
      element('[aria-label="Search projects…"]').dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }),
      )
    })
    expect(
      [...document.querySelectorAll('[role="option"]')].map((option) => option.textContent),
    ).toEqual(remaining)
    await clickButton('Share model')
  }

  expect(projects.map((project) => api.requestsTo('POST', grantPath(project.id)).length)).toEqual([
    1, 2, 3,
  ])
  expect(document.querySelector('[role="dialog"]')).toBeNull()
})

const credentialSecret = {
  id: provider.credential_secret_id,
  org_id: orgId,
  management_kind: 'tenant',
  owner: { kind: 'org' },
  name: 'openai-api-key',
  kind: 'generic',
  metadata: {},
  current_version_number: 1,
  payload_keys: ['value'],
  created_at: timestamp,
  updated_at: timestamp,
}
const providersPath = `/api/v1/orgs/${orgId}/model-provider-configs`

function providerApi() {
  return creationApi({
    extraRoutes: [
      {
        method: 'GET',
        path: `/api/v1/orgs/${orgId}/secrets`,
        respond: () => jsonResponse({ data: [credentialSecret], next_cursor: null }),
      },
      {
        method: 'GET',
        path: `/api/v1/orgs/${orgId}/secrets/${credentialSecret.id}`,
        respond: () => jsonResponse(credentialSecret),
      },
      {
        method: 'POST',
        path: providersPath,
        respond: () =>
          jsonResponse(
            { config: provider, model_catalog: { status: 'ok', models: discoveredModels } },
            201,
          ),
      },
    ],
  })
}

it('creates a preset provider and continues straight into the model picker', async () => {
  const api = providerApi()
  await render(api, <CreateModelProviderDialog open onOpenChange={vi.fn()} orgId={orgId} />)

  expect(element('#mp-provider').textContent).toContain('OpenAI')
  await type('#mp-name', 'production-openai')
  await selectOption('[aria-label="Search secrets…"]', 'openai-api-key')
  await clickButton('Add provider')

  expect(api.requestsTo('POST', providersPath).map((request) => request.body)).toEqual([
    { name: 'production-openai', credential_secret_id: credentialSecret.id, preset: 'openai' },
  ])
  await waitForUI(() => {
    expect(modelRows()).toContain('model-one100K')
  })
  expect(api.requestsTo('GET', catalogPath)).toEqual([])
  expect(button('Skip for now')).toBeDefined()
})

it('edits a preset endpoint as a custom endpoint starting from the preset', async () => {
  await render(
    providerApi(),
    <CreateModelProviderDialog open onOpenChange={vi.fn()} orgId={orgId} />,
  )
  await clickButton('Advanced')
  expect(document.body.textContent).toContain('https://api.openai.com/v1 · openai-responses')
  await clickButton('Use a custom endpoint')

  expect(element('#mp-provider').textContent).toContain('Custom endpoint')
  expect(element('#mp-base-url')).toHaveProperty('value', 'https://api.openai.com/v1')
  expect(element('#mp-api-format').textContent).toContain('OpenAI Responses')
})
