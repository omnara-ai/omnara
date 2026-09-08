/** @vitest-environment happy-dom */
import { type ModelProviderListSort, OmnaraClientProvider, useModelProviders } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, useEffect } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { useArrayPagination } from '@/hooks/use-array-pagination'
import { usePagedQuery } from '@/hooks/use-paged-query'
import {
  filterAndSortLocalItems,
  resourceSortOptions,
  useListToolbarVisibility,
  useResourceList,
} from '@/hooks/use-resource-list'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { enableReactActEnvironment } from '@/test/react-act'

const orgId = `org_${'a'.repeat(26)}`
const providers = Array.from({ length: 15 }, (_, i) => ({
  id: `mpc_${String.fromCharCode(97 + i).repeat(26)}`,
  org_id: orgId,
  management_kind: 'tenant',
  name: `provider-${i}`,
  api_format: 'openai-responses',
  api_variant: 'default',
  base_url: 'https://example.test',
  endpoint_path: '/responses',
  request_timeout_ms: 1000,
  auth_kind: 'bearer_token',
  auth_options: {},
  credential_secret_id: `sec_${'a'.repeat(26)}`,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}))
let container: HTMLDivElement
let root: Root
let queryClient: QueryClient
let restore: () => void
type ListControls = ReturnType<typeof useResourceList<ModelProviderListSort>>
let controls: ListControls
let next: () => void
let queryStatus: string
let rejectSort: (reason: Error) => void
let resolveSort: (response: Response) => void

beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
  queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30000 } } })
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  queryClient.clear()
  container.remove()
  restore()
})
async function flush(ms = 10) {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms))
  })
}
function Header({ visible, controls }: { visible: boolean; controls: ListControls }) {
  return (
    <SearchHeader
      title="Providers"
      toolbar={
        visible ? (
          <ResourceListToolbar
            search={controls.search}
            onSearchChange={controls.setSearch}
            sort={controls.sort}
            sortOptions={resourceSortOptions}
            onSortChange={controls.setSort}
            placeholder="Search providers"
          />
        ) : undefined
      }
    >
      <button>New provider</button>
    </SearchHeader>
  )
}
function ServerList() {
  const list = useResourceList<ModelProviderListSort>('-created_at')
  const query = useModelProviders(orgId, { filters: list.apiFilters, sort: list.sort })
  const paged = usePagedQuery(query, list.queryKey)
  const visible = useListToolbarVisibility(list, paged.pagination, query.isSuccess)
  useEffect(() => {
    controls = list
    queryStatus = query.status
    next = paged.pagination.onNext
  }, [list, paged.pagination.onNext, query.status])
  return <Header visible={visible} controls={list} />
}
function element(selector: string): HTMLElement {
  const match = container.querySelector(selector)
  if (!(match instanceof HTMLElement)) throw new Error(`Missing element: ${selector}`)
  return match
}
async function mountServer(multiplePages = true) {
  const api = fakeApi([
    {
      method: 'GET',
      path: `/api/v1/orgs/${orgId}/model-provider-configs`,
      respond: (request) => {
        if (request.url.searchParams.has('name'))
          return jsonResponse({ data: [], next_cursor: null })
        if (request.url.searchParams.get('sort') === 'name') {
          return new Promise<Response>((resolve, reject) => {
            resolveSort = resolve
            rejectSort = reject
          })
        }
        return jsonResponse({
          data: providers,
          next_cursor: !multiplePages || request.url.searchParams.has('cursor') ? null : 'next',
        })
      },
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ServerList />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
  expect(container.querySelector('input')).toBeNull()
  await flush()
  await flush()
  expect(queryStatus).toBe('success')
}
it('preserves sort focus while loading a new sort and after it succeeds', async () => {
  await mountServer()
  const trigger = element('[role="combobox"]')
  trigger.focus()
  act(() => {
    controls.setSort('name')
  })
  await flush()
  expect(queryStatus).toBe('pending')
  expect(trigger.isConnected).toBe(true)
  expect(document.activeElement).toBe(trigger)
  act(() => {
    resolveSort(jsonResponse({ data: providers, next_cursor: 'next' }))
  })
  await flush()
  expect(queryStatus).toBe('success')
  expect(container.querySelector('[role="combobox"]')).toBe(trigger)
  expect(document.activeElement).toBe(trigger)
})
it('keeps controls after a sort request fails and allows returning to the cached sort', async () => {
  await mountServer()
  act(() => {
    controls.setSort('name')
  })
  await flush()
  act(() => {
    rejectSort(new Error('Temporary API failure'))
  })
  await flush()
  expect(queryStatus).toBe('error')
  expect(container.querySelector('[role="combobox"]')).not.toBeNull()
  act(() => {
    controls.setSort('-created_at')
  })
  await flush()
  expect(queryStatus).toBe('success')
  expect(container.querySelector('input')).not.toBeNull()
})
it('keeps the same focused search input when a server search returns no rows', async () => {
  await mountServer()
  const input = element('input')
  input.focus()
  act(() => {
    controls.setSearch('no-match')
  })
  await flush(280)
  await flush()
  expect(queryStatus).toBe('success')
  expect(container.querySelector('input')).toBe(input)
  expect(document.activeElement).toBe(input)
})
it('keeps controls on the last server page', async () => {
  await mountServer()
  act(() => {
    next()
  })
  await flush()
  await flush()
  expect(container.querySelector('input')).not.toBeNull()
})
function LocalList({ count = 16 }: { count?: number }) {
  const list = useResourceList<ModelProviderListSort>('name')
  const items = Array.from({ length: count }, (_, i) => ({ id: String(i), name: `model-${i}` }))
  const filtered = filterAndSortLocalItems(items, list, { name: (item) => item.name })
  const paged = useArrayPagination(filtered, (item) => item.id)
  const visible = useListToolbarVisibility(list, paged.pagination, true)
  useEffect(() => {
    controls = list
  }, [list])
  return <Header visible={visible} controls={list} />
}
it('preserves local search focus before and after debounce and clearing', async () => {
  act(() => {
    root.render(<LocalList />)
  })
  const input = element('input')
  input.focus()
  act(() => {
    controls.setSearch('model-15')
  })
  expect(document.activeElement).toBe(input)
  await flush(280)
  expect(document.activeElement).toBe(input)
  act(() => {
    controls.setSearch('')
  })
  await flush(280)
  expect(document.activeElement).toBe(input)
})
it('keeps action buttons when controls are hidden', () => {
  act(() => {
    root.render(<LocalList count={0} />)
  })
  expect(container.querySelector('input')).toBeNull()
  expect(container.querySelector('button')?.textContent).toBe('New provider')
})

it('hides controls only after a new sort successfully returns a single page', async () => {
  await mountServer()
  act(() => {
    controls.setSort('name')
  })
  await flush()
  expect(container.querySelector('input')).not.toBeNull()
  act(() => {
    resolveSort(jsonResponse({ data: providers, next_cursor: null }))
  })
  await flush()
  expect(queryStatus).toBe('success')
  expect(container.querySelector('input')).toBeNull()
  expect(container.querySelector('button')?.textContent).toBe('New provider')
})
it('keeps small server lists hidden after loading', async () => {
  await mountServer(false)
  expect(container.querySelector('input')).toBeNull()
  expect(container.querySelector('button')?.textContent).toBe('New provider')
})
it('preserves search focus when clearing a filter starts an uncached query that fails', async () => {
  await mountServer()
  const input = element('input')
  input.focus()
  act(() => {
    controls.setSearch('missing')
  })
  await flush(280)
  await flush()
  act(() => {
    controls.setSort('name')
  })
  await flush()
  await flush()
  expect(queryStatus).toBe('success')
  act(() => {
    controls.setSearch('')
  })
  await flush(280)
  expect(queryStatus).toBe('pending')
  expect(document.activeElement).toBe(input)
  act(() => {
    rejectSort(new Error('Temporary API failure'))
  })
  await flush()
  expect(queryStatus).toBe('error')
  expect(document.activeElement).toBe(input)
})
