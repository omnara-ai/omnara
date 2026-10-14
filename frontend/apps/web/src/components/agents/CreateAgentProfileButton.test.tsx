/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act, Suspense } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

import { OrgCreateAgentProfileButton } from './CreateAgentProfileButton'

const orgId = fakeId('org')
const timestamp = '2026-01-01T00:00:00Z'

function project(id: string, name: string) {
  return {
    id,
    org_id: orgId,
    name,
    access: { can_read: true, can_manage: true, can_manage_access: true, can_operate: true },
    created_at: timestamp,
    updated_at: timestamp,
  }
}

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

it('waits for every page of projects before choosing how to create', async () => {
  let releaseSecondPage: () => void = () => undefined
  const secondPage = new Promise<void>((resolve) => {
    releaseSecondPage = resolve
  })
  const client = createOmnaraClient({
    baseUrl: 'https://omnara.test/api/v1',
    fetch: async (input, init) => {
      const url = new URL(new Request(input, init).url)
      if (url.searchParams.get('cursor') === 'page-2') {
        await secondPage
        return jsonResponse({
          data: [project(`proj_${'b'.repeat(26)}`, 'Beta')],
          next_cursor: null,
        })
      }
      return jsonResponse({
        data: [project(`proj_${'a'.repeat(26)}`, 'Alpha')],
        next_cursor: 'page-2',
      })
    },
  })
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => <OrgCreateAgentProfileButton orgId={orgId} label="New agent" />,
    }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={new QueryClient()}>
          <Suspense fallback={null}>
            <RouterProvider router={router} />
          </Suspense>
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await new Promise((resolve) => setTimeout(resolve, 0))
  })

  // Only Alpha has loaded: a direct link to it would be wrong once Beta arrives.
  expect(container.querySelector('a')).toBeNull()
  expect(container.textContent).toBe('')

  releaseSecondPage()
  await waitForUI(() => {
    expect(container.querySelector('button')?.textContent).toBe('New agent')
  })
  expect(container.querySelector('a')).toBeNull()
})
