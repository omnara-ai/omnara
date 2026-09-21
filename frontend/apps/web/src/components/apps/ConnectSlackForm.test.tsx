/** @vitest-environment happy-dom */
import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import { getProjectAppQueryKey } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId, projectApp } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { enter, waitForUI } from '@/test/secret-editor'

import { ConnectSlackForm } from './ConnectSlackForm'

let root: Root, container: HTMLDivElement, cache: QueryClient, restore: () => void
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
  cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  cache.clear()
  container.remove()
  restore()
  window.history.replaceState(null, '', '/')
  vi.useRealTimers()
  vi.restoreAllMocks()
})

it.each(['changed', 'deleted'] as const)(
  'stops waiting when the app is %s during authorization',
  async (failure) => {
    vi.useFakeTimers()
    const orgId = fakeId('org'),
      projectId = fakeId('proj')
    const app = projectApp({ provider_tenant_id: 'T123', provider_account_ref: 'A123' })
    const path = `/api/v1/orgs/${orgId}/projects/${projectId}/apps/${app.id}`
    const api = fakeApi([
      {
        method: 'POST',
        path: path + '/oauth/setup',
        respond: () =>
          jsonResponse(
            {
              app_id: app.id,
              setup_revision: app.setup_revision,
              provider: 'slack',
              flow_id: fakeId('ioaf'),
              oauth_url: 'https://slack.com/oauth/v2/authorize',
              redirect_uri: 'https://omnara.test/callback',
              events_url: 'https://omnara.test/events',
              actions_url: 'https://omnara.test/actions',
              expires_at: new Date(Date.now() + 600_000).toISOString(),
            },
            201,
          ),
      },
      {
        method: 'GET',
        path,
        respond: () =>
          failure === 'deleted'
            ? jsonResponse({ code: 'not_found', error: 'not found' }, 404)
            : Response.json({
                ...app,
                state: 'active',
                setup_revision: app.setup_revision + 1,
                last_oauth_flow_id: `ioaf_${'b'.repeat(26)}`,
              }),
      },
    ])
    const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
    const onConnected = vi.fn()
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>
            <ConnectSlackForm
              app={app}
              orgId={orgId}
              projectId={projectId}
              onConnected={onConnected}
            />
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
    })
    await enter('Client ID', 'client')
    await enter('Client secret', 'secret')
    await enter('Signing secret', 'signature')
    await act(async () => {
      document
        .querySelector('form')
        ?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      await Promise.resolve()
    })
    await waitForUI(() => {
      expect(document.querySelector('[role="alert"]')?.textContent).toContain(
        failure === 'deleted'
          ? 'This app was deleted'
          : 'setup changed while authorization was open',
      )
    })
    expect(document.body.textContent).not.toContain('Authorize in Slack')
    expect(onConnected).not.toHaveBeenCalled()

    const reads = api.requestsTo('GET', path).length
    expect(reads).toBeGreaterThan(0)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000)
    })
    expect(api.requestsTo('GET', path)).toHaveLength(reads)
  },
)

it.each(['complete', 'invalidate'] as const)(
  'uses the server-captured revision with an older cached app, then handles %s',
  async (outcome) => {
    vi.useFakeTimers()
    const orgId = fakeId('org'),
      projectId = fakeId('proj'),
      flowId = fakeId('ioaf')
    const cached = projectApp({
      state: 'disconnected',
      setup_revision: 1,
      provider_tenant_id: 'T123',
      provider_account_ref: 'A123',
    })
    const path = `/api/v1/orgs/${orgId}/projects/${projectId}/apps/${cached.id}`
    let current = { ...cached, setup_revision: 2 }
    let releaseRead!: (response: Response) => void
    const firstRead = new Promise<Response>((resolve) => {
      releaseRead = resolve
    })
    let reads = 0
    const api = fakeApi([
      {
        method: 'POST',
        path: path + '/oauth/setup',
        respond: () =>
          jsonResponse(
            {
              app_id: cached.id,
              setup_revision: 2,
              provider: 'slack',
              flow_id: flowId,
              oauth_url: 'https://slack.com/oauth/v2/authorize',
              redirect_uri: 'https://omnara.test/callback',
              events_url: 'https://omnara.test/events',
              actions_url: 'https://omnara.test/actions',
              expires_at: new Date(Date.now() + 600_000).toISOString(),
            },
            201,
          ),
      },
      {
        method: 'GET',
        path,
        respond: () => (++reads === 1 ? firstRead : Response.json(current)),
      },
    ])
    const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
    cache.setQueryData(
      getProjectAppQueryKey({
        path: { orgID: orgId, projectID: projectId, appID: cached.id },
        client,
      }),
      cached,
    )
    const onConnected = vi.fn()
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>
            <ConnectSlackForm
              app={cached}
              orgId={orgId}
              projectId={projectId}
              onConnected={onConnected}
            />
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
    })
    await enter('Client ID', 'client')
    await enter('Client secret', 'secret')
    await enter('Signing secret', 'signature')
    await act(async () => {
      document
        .querySelector('form')
        ?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      await Promise.resolve()
    })
    await waitForUI(() => {
      expect(api.requestsTo('GET', path)).toHaveLength(1)
      expect(document.body.textContent).toContain('Authorize in Slack')
    })
    expect(document.body.textContent).not.toContain('setup changed while authorization was open')
    await act(async () => {
      releaseRead(Response.json(current))
      await firstRead
    })
    await waitForUI(() => {
      expect(document.body.textContent).toContain('Authorize in Slack')
    })
    expect(document.body.textContent).not.toContain('setup changed while authorization was open')
    expect(onConnected).not.toHaveBeenCalled()
    current = {
      ...current,
      state: 'active',
      setup_revision: 3,
      last_oauth_flow_id: outcome === 'complete' ? flowId : `ioaf_${'b'.repeat(26)}`,
    }
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000)
    })
    await waitForUI(() => {
      expect(reads).toBe(2)
      if (outcome === 'complete') {
        expect(onConnected).toHaveBeenCalledOnce()
      } else {
        expect(document.querySelector('[role="alert"]')?.textContent).toContain(
          'setup changed while authorization was open',
        )
        expect(onConnected).not.toHaveBeenCalled()
      }
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000)
    })
    expect(reads).toBe(2)
  },
)
