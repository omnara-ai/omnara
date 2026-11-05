/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it } from 'vitest'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

import { AgentPanelBootstrap } from './AgentPanelBootstrap'

it('waits for a model, attempts once in StrictMode, and retries the same request after failure', async () => {
  const restore = enableReactActEnvironment()
  const container = document.createElement('div')
  const root = createRoot(container)
  const orgID = fakeId('org')
  const path = `/api/v1/orgs/${orgID}/assistant-agents`
  let calls = 0
  const api = fakeApi([
    {
      method: 'POST',
      path,
      respond: () => {
        calls++
        return calls === 1
          ? jsonResponse({ code: 'service_unavailable', error: 'Try later' }, 503)
          : jsonResponse({
              agent_id: fakeId('agt'),
              panel: { state: 'empty', enabled: true, revision: 1 },
            })
      },
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const setup = {
    request_id: crypto.randomUUID(),
    ready: false,
    scope: { org_id: orgID, project_id: fakeId('proj'), org_name: 'Home', project_name: 'Default' },
  }
  async function render(ready: boolean) {
    await act(async () => {
      root.render(
        <StrictMode>
          <OmnaraClientProvider client={client}>
            <QueryClientProvider client={cache}>
              <AgentPanelBootstrap setup={{ ...setup, ready }} revision={0} />
            </QueryClientProvider>
          </OmnaraClientProvider>
        </StrictMode>,
      )
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
  }
  try {
    await render(false)
    expect(calls).toBe(0)
    await render(true)
    await render(true)
    expect(calls).toBe(1)
    expect(container.textContent).toContain('Try later')
    const retry = container.querySelector('button')
    expect(retry?.textContent).toBe('Retry setup')
    await act(async () => {
      retry?.click()
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    expect(calls).toBe(2)
    expect(api.requests[0]?.body).toEqual(api.requests[1]?.body)
  } finally {
    act(() => {
      root.unmount()
    })
    cache.clear()
    restore()
  }
})
