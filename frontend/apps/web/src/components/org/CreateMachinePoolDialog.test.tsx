/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type Secret } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { type FakeApi, fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId, machinePool, projectMachinePoolGrant } from '@/test/fixtures'
import { addProject } from '@/test/project-share-chips'
import { enableReactActEnvironment } from '@/test/react-act'
import { button } from '@/test/secret-editor'

import { CreateMachinePoolDialog } from './CreateMachinePoolDialog'

const timestamp = '2026-01-01T00:00:00Z'
const orgId = fakeId('org')
const secret = {
  id: fakeId('sec'),
  org_id: orgId,
  management_kind: 'tenant',
  owner: { kind: 'org' },
  name: 'BLAXEL_TOKEN',
  kind: 'generic',
  metadata: {},
  current_version_number: 1,
  payload_keys: ['value'],
  created_at: timestamp,
  updated_at: timestamp,
} satisfies Secret
const poolsPath = `/api/v1/orgs/${orgId}/machine-pools`
const createdPool = machinePool({ org_id: orgId, provider: 'blaxel' })
const project = {
  id: `proj_${'a'.repeat(26)}`,
  org_id: orgId,
  name: 'cli-agent',
  access: { can_read: true, can_manage: true, can_manage_access: true, can_operate: true },
  created_at: timestamp,
  updated_at: timestamp,
}
const grantPath = `/api/v1/orgs/${orgId}/projects/${project.id}/machine-pool-grants`

let container: HTMLDivElement
let root: Root
let restoreActEnvironment: () => void

beforeEach(() => {
  restoreActEnvironment = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  restoreActEnvironment()
})

function poolApi() {
  return fakeApi([
    {
      method: 'GET',
      path: `/api/v1/orgs/${orgId}/secrets`,
      respond: () => jsonResponse({ data: [secret], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `/api/v1/orgs/${orgId}/secrets/${secret.id}`,
      respond: () => jsonResponse(secret),
    },
    {
      method: 'GET',
      path: `/api/v1/orgs/${orgId}/projects`,
      respond: () => jsonResponse({ data: [project], next_cursor: null }),
    },
    {
      method: 'POST',
      path: grantPath,
      respond: () =>
        new Response(
          JSON.stringify(
            projectMachinePoolGrant({
              org_id: orgId,
              project_id: project.id,
              machine_pool_id: createdPool.id,
            }),
          ),
          { status: 201, headers: { 'Content-Type': 'application/json' } },
        ),
    },
    {
      method: 'POST',
      path: poolsPath,
      respond: () =>
        new Response(JSON.stringify(createdPool), {
          status: 201,
          headers: { 'Content-Type': 'application/json' },
        }),
    },
  ])
}

async function interact(action: () => void) {
  await act(async () => {
    action()
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

async function render(api: FakeApi, onOpenChange: (open: boolean) => void) {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  await interact(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <CreateMachinePoolDialog open onOpenChange={onOpenChange} orgId={orgId} />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
}

function input(selector: string) {
  const element = document.querySelector(selector)
  if (!(element instanceof HTMLInputElement)) throw new Error(`Missing input: ${selector}`)
  return element
}

async function type(selector: string, value: string) {
  await interact(() => {
    const element = input(selector)
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(element, value)
    element.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function click(name: string) {
  await interact(() => {
    button(name).click()
  })
}

async function chooseSecret(name: string) {
  await interact(() => {
    document
      .querySelector('[aria-label="Search secrets…"]')
      ?.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
  })
  await interact(() => {
    const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(
      (candidate) => candidate.textContent === name,
    )
    if (!option) throw new Error(`Missing option: ${name}`)
    option.click()
  })
}

function activeStep() {
  return document.querySelector('[aria-current="step"] [data-step-title]')?.textContent
}

async function toggleStep(title: string) {
  await interact(() => {
    const header = [
      ...document.querySelectorAll<HTMLButtonElement>('[data-slot="collapsible-trigger"]'),
    ].find((trigger) => trigger.querySelector('[data-step-title]')?.textContent === title)
    if (!header) throw new Error(`Missing step: ${title}`)
    header.click()
  })
}

function text() {
  return document.body.textContent
}

it('walks through each step, summarizing finished ones, and creates the pool early', async () => {
  const api = poolApi()
  const onOpenChange = vi.fn()
  await render(api, onOpenChange)

  expect(activeStep()).toBe('Provider')
  expect(text()).toContain('Step 1 of 4')
  expect(button('Continue').disabled).toBe(true)
  await type('#mpool-name', 'default')
  await chooseSecret('BLAXEL_TOKEN')
  await click('Continue')

  expect(activeStep()).toBe('Image')
  expect(text()).toContain('default · Blaxel · BLAXEL_TOKEN')
  expect(() => button('Skip & create')).toThrow()
  await type('#mpool-image', 'omnara/agent-sandbox')
  expect(button('Continue').disabled).toBe(true)
  await type('#mpool-provider-scope', 'acme')
  await click('Continue')

  expect(activeStep()).toBe('Capacity')
  expect(text()).toContain('omnara/agent-sandbox · acme')
  expect(text()).toContain('Up to 3 machines in us-pdx-1 · 3 GB total')
  await click('More machines')
  expect(text()).toContain('Up to 4 machines in us-pdx-1 · 4 GB total')

  await addProject('cli-agent')
  expect(button('Remove cli-agent')).toBeDefined()
  await toggleStep('Capacity')
  expect(document.querySelector('#mpool-memory')).toBeNull()
  expect(text()).toContain('Step 3 of 4')
  await toggleStep('Environment')
  expect(activeStep()).toBe('Environment')
  expect(document.querySelector('#mpool-startup-script')).not.toBeNull()
  await toggleStep('Provider')
  expect(activeStep()).toBe('Provider')
  expect(document.querySelector('#mpool-name')).not.toBeNull()
  expect(text()).toContain('us-pdx-1 · up to 4 × 1 GB')
  await click('Skip & create')

  const [created] = api.requestsTo('POST', poolsPath)
  expect(created?.body).toMatchObject({
    name: 'default',
    provider: 'blaxel',
    provider_auth_secret_id: secret.id,
    provider_config: { workspace: 'acme' },
    max_total_machines: 4,
    default_machine_memory_mb: 1024,
    default_machine_provider_options: { image: 'omnara/agent-sandbox', region: 'us-pdx-1' },
  })
  expect(api.requestsTo('POST', grantPath).map((request) => request.body)).toEqual([
    { machine_pool_id: createdPool.id },
  ])
  expect(onOpenChange).toHaveBeenCalledWith(false)
})

it('blocks the capacity step on an invalid max machine memory and shows why', async () => {
  const api = poolApi()
  await render(api, vi.fn())
  await type('#mpool-name', 'default')
  await chooseSecret('BLAXEL_TOKEN')
  await click('Continue')
  await type('#mpool-image', 'omnara/agent-sandbox')
  await type('#mpool-provider-scope', 'acme')
  await click('Continue')
  expect(activeStep()).toBe('Capacity')
  expect(button('Continue').disabled).toBe(false)

  await click('Advanced')
  expect(button('Advanced').getAttribute('aria-expanded')).toBe('true')
  await type('#mpool-max-machine-memory', '0')
  expect(button('Continue').disabled).toBe(true)
  expect(() => button('Skip & create')).toThrow()
  expect(input('#mpool-max-machine-memory').getAttribute('aria-invalid')).toBe('true')
  expect(text()).toContain('Enter a size greater than 0 GB, or leave it empty.')
  // The section can't be folded away while it hides the error blocking the step.
  await click('Advanced')
  expect(button('Advanced').getAttribute('aria-expanded')).toBe('true')

  await type('#mpool-max-machine-memory', '2')
  expect(button('Continue').disabled).toBe(false)
  expect(text()).not.toContain('Enter a size greater than 0 GB')
  await click('Advanced')
  expect(button('Advanced').getAttribute('aria-expanded')).toBe('false')
})
