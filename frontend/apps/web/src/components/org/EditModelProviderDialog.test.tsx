/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type ModelProviderConfig } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, waitForUI } from '@/test/secret-editor'

import { EditModelProviderDialog } from './EditModelProviderDialog'

const timestamp = '2026-01-01T00:00:00Z'
const gatewaySecretId = `sec_${'b'.repeat(26)}`
const provider = {
  id: fakeId('mpc'),
  org_id: fakeId('org'),
  management_kind: 'tenant',
  name: 'gateway',
  api_format: 'openai-responses',
  api_variant: 'openai',
  base_url: 'https://gateway.example.com/v1',
  endpoint_path: '/responses',
  request_timeout_ms: 120000,
  idle_timeout_ms: 300000,
  auth_kind: 'bearer_token',
  auth_options: {},
  credential_secret_id: fakeId('sec'),
  headers: { 'X-Team': 'platform' },
  secret_headers: { 'X-Gateway-Key': gatewaySecretId },
  created_at: timestamp,
  updated_at: timestamp,
} satisfies ModelProviderConfig
const providerPath = `/api/v1/orgs/${provider.org_id}/model-provider-configs/${provider.id}`

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

it('replaces provider headers with the edited rows', async () => {
  const api = fakeApi([
    { method: 'PUT', path: providerPath, respond: () => jsonResponse(provider) },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const onOpenChange = vi.fn()
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <EditModelProviderDialog
            open
            onOpenChange={onOpenChange}
            orgId={provider.org_id}
            provider={provider}
          />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await Promise.resolve()
  })

  await act(async () => {
    button('Remove header').click()
    await Promise.resolve()
  })
  await act(async () => {
    button('Add header').click()
    await Promise.resolve()
  })
  await enter('Header name', 'X-Org')
  await enter('Header value', 'acme')
  await act(async () => {
    button('Save changes').click()
    await Promise.resolve()
  })

  await waitForUI(() => {
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })
  expect(api.requestsTo('PUT', providerPath).map((request) => request.body)).toEqual([
    {
      base_url: provider.base_url,
      endpoint_path: provider.endpoint_path,
      request_timeout_ms: provider.request_timeout_ms,
      idle_timeout_ms: provider.idle_timeout_ms,
      headers: { 'X-Org': 'acme' },
      secret_headers: { 'X-Gateway-Key': gatewaySecretId },
    },
  ])
})
