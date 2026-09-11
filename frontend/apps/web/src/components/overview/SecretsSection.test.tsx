/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type Secret } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { SecretsSection } from '@/components/overview/SecretsSection'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, field, submit, waitForUI } from '@/test/secret-editor'

const secret: Secret = {
  id: fakeId('sec'),
  org_id: fakeId('org'),
  management_kind: 'tenant',
  owner: { kind: 'org' },
  name: 'token',
  kind: 'generic',
  metadata: {},
  current_version_number: 1,
  payload_keys: ['value'],
  created_at: '2026-08-03T00:00:00Z',
  updated_at: '2026-08-03T00:00:00Z',
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
it('retains the editor and retries value-only after rename removes the filtered row', async () => {
  const path = `/api/v1/orgs/${secret.org_id}/secrets/${secret.id}`
  let renamed = false
  let attempts = 0
  const api = fakeApi([
    {
      method: 'GET',
      path: `/api/v1/orgs/${secret.org_id}/secrets`,
      respond: () =>
        jsonResponse({ data: renamed ? [] : [secret], next_cursor: renamed ? null : 'next' }),
    },
    {
      method: 'PATCH',
      path,
      respond: () => {
        renamed = true
        return jsonResponse({ ...secret, name: 'renamed' })
      },
    },
    {
      method: 'POST',
      path: path + '/versions',
      respond: () =>
        ++attempts === 1
          ? jsonResponse({ code: 'conflict', error: 'Try again' }, 409)
          : jsonResponse({ ...secret, name: 'renamed', current_version_number: 2 }),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ActiveOrgContext
            value={{
              orgs: [],
              activeOrg: {
                id: secret.org_id,
                name: 'Test',
                role: 'owner',
                created_at: secret.created_at,
              },
              setActiveOrgId: () => undefined,
            }}
          >
            <SecretsSection />
          </ActiveOrgContext>
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await Promise.resolve()
  })
  await waitForUI(() => {
    expect(button('Secret actions')).toBeDefined()
  })
  await enter('Search secrets by name…', 'token')
  await waitForUI(() => {
    expect(
      api
        .requestsTo('GET', `/api/v1/orgs/${secret.org_id}/secrets`)
        .some((request) => request.url.search.includes('token')),
    ).toBe(true)
    expect(button('Secret actions')).toBeDefined()
  })
  act(() => {
    button('Secret actions').dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }),
    )
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="menuitem"]')).not.toBeNull()
  })
  act(() => {
    const edit = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
      (item) => item.textContent.trim() === 'Edit',
    )
    if (!edit) throw new Error('Missing Edit action')
    edit.click()
  })
  await waitForUI(() => {
    expect(field('Name')).toBeDefined()
  })
  await enter('Name', 'renamed')
  await enter('Value', 'replacement')
  await submit()
  expect(document.querySelector('button[aria-label="Secret actions"]')).toBeNull()
  expect(document.querySelector('[role="dialog"]')).not.toBeNull()
  expect(document.body.textContent).toContain('Name saved, but the value update failed.')
  expect(field('Value').value).toBe('replacement')
  await submit()
  expect(api.requestsTo('PATCH', path)).toHaveLength(1)
  expect(api.requestsTo('POST', path + '/versions')).toHaveLength(2)
  expect(document.querySelector('[role="dialog"]')).toBeNull()
  queryClient.clear()
})
