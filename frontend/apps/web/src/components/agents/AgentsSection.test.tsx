/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'

import { AgentsSection } from '@/components/agents/AgentsSection'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

const orgId = fakeId('org')
const projectId = fakeId('proj')

let container: HTMLDivElement
let root: Root
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
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
})

function menuItem(role: string, name: string) {
  const item = Array.from(document.querySelectorAll<HTMLElement>(`[role="${role}"]`)).find(
    (candidate) => candidate.textContent.includes(name),
  )
  if (!item) throw new Error(`No ${role} named ${name}`)
  return item
}

it('shows no results, not the empty copy, when a status filter matches nothing', async () => {
  const api = fakeApi([
    {
      method: 'GET',
      path: `/api/v1/orgs/${orgId}/projects/${projectId}/agents`,
      respond: () => jsonResponse({ data: [], next_cursor: null }),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <AgentsSection
            orgId={orgId}
            projectId={projectId}
            canManage
            emptyMessage="No agents yet."
            emptyAction={<a href="/launch">Launch an agent</a>}
          />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
  await vi.waitFor(() => {
    expect(container.textContent).toContain('No agents yet.')
  })
  expect(container.textContent).toContain('Launch an agent')

  const filters = Array.from(container.querySelectorAll('button')).find((button) =>
    button.textContent.includes('Filters'),
  )
  if (!filters) throw new Error('No filters button')
  act(() => {
    filters.focus()
    filters.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
  })
  act(() => {
    menuItem('menuitem', 'Status').click()
  })
  act(() => {
    menuItem('menuitemradio', 'Idle').click()
  })

  await vi.waitFor(() => {
    expect(api.requests.at(-1)?.url.searchParams.get('activity_state')).toBe('idle')
  })
  expect(container.textContent).toContain('No results.')
  expect(container.textContent).not.toContain('No agents yet.')
  expect(container.textContent).not.toContain('Launch an agent')
})
