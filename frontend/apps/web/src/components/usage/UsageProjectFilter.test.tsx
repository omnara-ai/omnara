/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type VisibleProject } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { allProjectsUsageFilter } from '@/components/usage/usage-project-filter'
import { UsageProjectMenuItems } from '@/components/usage/UsageProjectFilter'
import { fakeApi, type FakeRoute, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

const orgId = fakeId('org')
const path = `/api/v1/orgs/${orgId}/projects`
const first: VisibleProject = {
  id: fakeId('proj'),
  org_id: orgId,
  name: 'First project',
  created_at: '2026-09-15T00:00:00Z',
  updated_at: '2026-09-15T00:00:00Z',
  access: { can_read: true, can_manage: true, can_manage_access: true, can_operate: true },
}
const second: VisibleProject = { ...first, id: `proj_${'b'.repeat(26)}`, name: 'Later project' }
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
  restore()
})

function ProjectMenu() {
  const [value, setValue] = useState(allProjectsUsageFilter)
  return (
    <DropdownMenu defaultOpen>
      <DropdownMenuTrigger>Projects</DropdownMenuTrigger>
      <DropdownMenuContent>
        <UsageProjectMenuItems orgId={orgId} value={value} onChange={setValue} />
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

async function render(respond: FakeRoute['respond']) {
  const api = fakeApi([{ method: 'GET', path, respond }])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ProjectMenu />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await Promise.resolve()
  })
  return api
}

function item(name: string) {
  const element = [...document.querySelectorAll<HTMLElement>('[role^="menuitem"]')].find(
    (element) => element.textContent.trim() === name,
  )
  if (!element) throw new Error(`Missing menu item: ${name}`)
  return element
}

it('loads the next cursor without closing the menu, retaining selections across pages', async () => {
  let finishPage!: (response: Response) => void
  const nextPage = new Promise<Response>((resolve) => {
    finishPage = resolve
  })
  const api = await render((request) =>
    request.url.searchParams.has('cursor')
      ? nextPage
      : jsonResponse({ data: [first], next_cursor: 'page-2' }),
  )
  await waitForUI(() => {
    expect(item(first.name)).toBeDefined()
  })
  act(() => {
    item(first.name).click()
  })
  act(() => {
    item('Load more projects').click()
  })
  await waitForUI(() => {
    expect(item('Loading…').getAttribute('aria-disabled')).toBe('true')
  })
  await act(async () => {
    finishPage(jsonResponse({ data: [second], next_cursor: null }))
    await Promise.resolve()
  })
  await waitForUI(() => {
    expect(item(second.name)).toBeDefined()
  })
  expect(item(first.name).getAttribute('aria-checked')).toBe('true')
  act(() => {
    item(second.name).click()
  })
  expect(item(second.name).getAttribute('aria-checked')).toBe('true')
  expect(item(first.name).getAttribute('aria-checked')).toBe('true')
  expect(document.querySelector('[role="menuitem"]')).toBeNull()
  expect(
    api.requestsTo('GET', path).map((request) => request.url.searchParams.get('cursor')),
  ).toEqual([null, 'page-2'])
  act(() => {
    item('All projects').click()
  })
  expect(item(first.name).getAttribute('aria-checked')).toBe('false')
  expect(item(second.name).getAttribute('aria-checked')).toBe('false')
})

it('retries a failed next page without discarding loaded projects or selections', async () => {
  let attempts = 0
  const api = await render((request) => {
    if (!request.url.searchParams.has('cursor'))
      return jsonResponse({ data: [first], next_cursor: 'page-2' })
    return ++attempts === 1
      ? jsonResponse({ code: 'service_unavailable', error: 'Try again' }, 503)
      : jsonResponse({ data: [second], next_cursor: null })
  })
  await waitForUI(() => {
    expect(item(first.name)).toBeDefined()
  })
  act(() => {
    item(first.name).click()
  })
  act(() => {
    item('Load more projects').click()
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="alert"]')?.textContent).toContain(
      'Could not load projects.',
    )
  })
  expect(item(first.name).getAttribute('aria-checked')).toBe('true')
  act(() => {
    item('Retry').click()
  })
  await waitForUI(() => {
    expect(item(second.name)).toBeDefined()
  })
  expect(item(first.name).getAttribute('aria-checked')).toBe('true')
  expect(document.querySelector('[role="alert"]')).toBeNull()
  expect(
    api.requestsTo('GET', path).map((request) => request.url.searchParams.get('cursor')),
  ).toEqual([null, 'page-2', 'page-2'])
})

it('offers a retry instead of an empty list after an initial load failure', async () => {
  let attempts = 0
  await render(() =>
    ++attempts === 1
      ? jsonResponse({ code: 'service_unavailable', error: 'Try again' }, 503)
      : jsonResponse({ data: [first], next_cursor: null }),
  )
  await waitForUI(() => {
    expect(item('Retry')).toBeDefined()
  })
  expect(document.body.textContent).not.toContain('No projects available.')
  act(() => {
    item('Retry').click()
  })
  await waitForUI(() => {
    expect(item(first.name)).toBeDefined()
  })
  expect(document.querySelector('[role="alert"]')).toBeNull()
})
