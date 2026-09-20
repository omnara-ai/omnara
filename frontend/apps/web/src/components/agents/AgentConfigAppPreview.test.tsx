/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, schemas } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it } from 'vitest'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

import { createBasicConfigSession, useAgentBuilderForm } from './useAgentBuilderForm'

it('sends scoped app selection to preview and exposes the resolver issue to the builder', async () => {
  const tool = { config: { channel_id: 'C123', thread_ts: '123.456' } }
  const scope = { orgId: fakeId('org'), projectId: fakeId('proj') }
  const source = JSON.stringify({ instruction: 'Review', tools: { app__chat__post_message: tool } })
  const api = fakeApi([
    {
      method: 'POST',
      path: `/api/v1/orgs/${scope.orgId}/projects/${scope.projectId}/agent-configs/tools`,
      respond: ({ body }) => {
        const request = schemas.zResolveAgentConfigToolsRequest.parse(body)
        expect(JSON.parse(request.source)).toHaveProperty('tools.app__chat__post_message', tool)
        return jsonResponse(
          {
            code: 'invalid_request',
            error: 'Invalid app tool',
            issues: [{ path: '/tools/app__chat__post_message', message: 'App not found' }],
          },
          400,
        )
      },
    },
  ])
  function Builder() {
    const form = useAgentBuilderForm(createBasicConfigSession(source), undefined, scope)
    return <output>{form.toolsError ? form.toolsErrorMessage : 'Resolving tools'}</output>
  }
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const container = document.createElement('div')
  const root = createRoot(container)
  const restore = enableReactActEnvironment()
  try {
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>
            <Builder />
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
    })
    await waitForUI(() => {
      expect(container.textContent).toContain('/tools/app__chat__post_message: App not found')
    })
    expect(api.requests).toHaveLength(1)
  } finally {
    act(() => {
      root.unmount()
    })
    cache.clear()
    restore()
  }
})
