/** @vitest-environment happy-dom */

import { OmnaraClientProvider, useCreateIntegrationSubscription } from '@omnara/react'
import {
  createOmnaraClient,
  type IntegrationSubscription,
  type ProjectIntegration,
} from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterContextProvider,
} from '@tanstack/react-router'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { fakeApi } from '@/test/fake-api'
import { fakeId, projectIntegration } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, waitForUI } from '@/test/secret-editor'

import { ProjectIntegrationConversations } from './ProjectIntegrationConversations'

const orgId = fakeId('org'),
  projectId = fakeId('proj')
const integration = projectIntegration({ state: 'active', provider_tenant_id: 'T123' })
const path = `/api/v1/orgs/${orgId}/projects/${projectId}/integrations/${integration.id}/subscriptions`
const subscription: IntegrationSubscription = {
  id: fakeId('isub'),
  project_id: projectId,
  integration_id: integration.id,
  agent_id: fakeId('agt'),
  agent_name: 'Support agent',
  type: 'thread_messages',
  conversation: { channel_id: 'C123', thread_ts: '111.222333' },
  events: ['message'],
  created_at: '2026-09-20T12:00:00Z',
}
const second: IntegrationSubscription = {
  ...subscription,
  id: `isub_${'b'.repeat(26)}`,
  agent_name: '',
  agent_id: `agt_${'b'.repeat(26)}`,
  conversation: { channel_id: 'C456' },
}
const stopLabel = 'Stop forwarding Channel C123 · Thread 111.222333 to Support agent'
let root: Root, container: HTMLDivElement, restore: () => void
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
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function conversations(currentIntegration: ProjectIntegration = integration, canManage = true) {
  return (
    <ProjectIntegrationConversations
      orgId={orgId}
      projectId={projectId}
      integration={currentIntegration}
      canManage={canManage}
    />
  )
}
function render(api: ReturnType<typeof fakeApi>, content = conversations()) {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  const cache = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: 30_000 } },
  })
  const router = createRouter({ routeTree: createRootRoute(), history: createMemoryHistory() })
  function rerender(content: ReactNode) {
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>
            <RouterContextProvider router={router}>{content}</RouterContextProvider>
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
    })
  }
  rerender(content)
  return { cache, rerender }
}

it('shows loading, retries an initial error and then explains an empty list', async () => {
  let release!: (response: Response) => void
  const pending = new Promise<Response>((resolve) => {
    release = resolve
  })
  let attempts = 0
  const api = fakeApi([
    {
      method: 'GET',
      path,
      respond: () => (++attempts === 1 ? pending : Response.json({ data: [], next_cursor: null })),
    },
  ])
  render(api)
  expect(container.querySelector('[role="status"]')?.textContent).toContain('Loading conversations')
  await act(async () => {
    release(Response.json({ code: 'internal_error', error: 'Try again' }, { status: 500 }))
    await Promise.resolve()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Could not load conversations.')
  })
  expect(container.textContent).not.toContain('No conversations yet.')
  act(() => {
    button('Retry conversations').click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('No conversations yet.')
  })
  expect(attempts).toBe(2)
  expect(container.querySelector('[role="alert"]')).toBeNull()
})

it('keeps earlier conversations while retrying the next cursor, with links and an ID fallback', async () => {
  let nextAttempts = 0
  const api = fakeApi([
    {
      method: 'GET',
      path,
      respond: ({ url }) => {
        expect(url.searchParams.get('limit')).toBe('15')
        if (!url.searchParams.has('cursor'))
          return Response.json({ data: [subscription], next_cursor: 'next-page' })
        expect(url.searchParams.get('cursor')).toBe('next-page')
        if (++nextAttempts === 1)
          return Response.json({ code: 'internal_error', error: 'Try again' }, { status: 500 })
        return Response.json({ data: [second], next_cursor: null })
      },
    },
  ])
  render(api)
  await waitForUI(() => {
    expect(container.textContent).toContain('Support agent')
  })
  const links = [...container.querySelectorAll('a')]
  expect(links.find((a) => a.textContent === 'Support agent')?.getAttribute('href')).toBe(
    `/projects/${projectId}/agents/${subscription.agent_id}/events`,
  )
  expect(
    links.find((a) => a.textContent === 'Channel C123 · Thread 111.222333')?.getAttribute('href'),
  ).toBe('https://slack.com/app_redirect?channel=C123&team=T123')
  expect(container.querySelector('time')?.dateTime).toBe(subscription.created_at)
  act(() => {
    button('Load more conversations').click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Could not load more conversations.')
  })
  expect(container.textContent).toContain('Support agent')
  act(() => {
    button('Retry conversations').click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain(second.agent_id)
  })
  expect(container.querySelectorAll('li')).toHaveLength(2)
  expect(container.textContent).not.toContain('Load more conversations')
  expect(api.requestsTo('GET', path)).toHaveLength(3)
})

it.each([
  {
    integrationType: 'discord_thread',
    conversation: { channel_id: '444444444444444444' },
    label: 'Channel 444444444444444444',
  },
  {
    integrationType: 'discord_thread',
    conversation: { channel_id: '444444444444444444', thread_id: '555555555555555555' },
    label: 'Channel 444444444444444444 · Thread 555555555555555555',
  },
  {
    integrationType: 'github_pr',
    conversation: { repository_id: 123, pull_request: 42 },
    label: 'Repository 123 · PR #42',
  },
] as const)(
  'shows a canonical $integrationType address without an invented link or mutation controls',
  async ({ integrationType, conversation, label }) => {
    const row = { ...subscription, conversation }
    const api = fakeApi([
      { method: 'GET', path, respond: () => Response.json({ data: [row], next_cursor: null }) },
    ])
    render(api, conversations(projectIntegration({ integration_type: integrationType }), false))
    await waitForUI(() => {
      expect(container.textContent).toContain(label)
    })
    expect(container.querySelectorAll('button')).toHaveLength(0)
    const link = [...container.querySelectorAll('a')].find((a) => a.textContent === label)
    expect(link).toBeUndefined()
    expect(container.querySelectorAll('a')).toHaveLength(1)
    expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
  },
)

it('allows stopping while disconnected, keeps failed deletions visible, and respects canceled confirmation and lost manage permission', async () => {
  let attempts = 0,
    deleted = false
  let release!: (response: Response) => void
  const pending = new Promise<Response>((resolve) => {
    release = resolve
  })
  const api = fakeApi([
    {
      method: 'GET',
      path,
      respond: () => Response.json({ data: deleted ? [] : [subscription], next_cursor: null }),
    },
    {
      method: 'DELETE',
      path: `${path}/${subscription.id}`,
      respond: () => {
        if (++attempts === 1)
          return Response.json(
            { code: 'internal_error', error: 'Please try stopping again' },
            { status: 500 },
          )
        deleted = true
        return pending
      },
    },
  ])
  const offline = { ...integration, state: 'disconnected' } satisfies ProjectIntegration
  const { rerender } = render(api, conversations(offline))
  await waitForUI(() => {
    expect(button(stopLabel).disabled).toBe(false)
  })
  expect(container.textContent).toContain('Forwarding is paused')
  vi.stubGlobal('confirm', () => false)
  act(() => {
    button(stopLabel).click()
  })
  expect(attempts).toBe(0)
  vi.stubGlobal('confirm', () => true)
  act(() => {
    button(stopLabel).click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Please try stopping again')
  })
  expect(container.textContent).toContain('Support agent')
  rerender(conversations(offline, false))
  expect(container.querySelectorAll('button')).toHaveLength(0)
  rerender(conversations(offline))
  act(() => {
    button(stopLabel).click()
  })
  await waitForUI(() => {
    expect(button(stopLabel).disabled).toBe(true)
  })
  await act(async () => {
    release(new Response(null, { status: 204 }))
    await Promise.resolve()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('No conversations yet.')
  })
  expect(container.textContent).not.toContain('Please try stopping again')
  expect(attempts).toBe(2)
  expect(
    api.requests
      .filter((request) => request.method !== 'GET')
      .every(
        (request) =>
          request.method === 'DELETE' && request.url.pathname === `${path}/${subscription.id}`,
      ),
  ).toBe(true)
})

it('does not resurrect a stopped conversation from a delayed GET or a failed refresh', async () => {
  let reads = 0,
    deleted = false
  let release!: (response: Response) => void
  const pending = new Promise<Response>((resolve) => {
    release = resolve
  })
  const api = fakeApi([
    {
      method: 'GET',
      path,
      respond: () => {
        reads++
        if (deleted)
          return Response.json({ code: 'internal_error', error: 'Offline' }, { status: 500 })
        return reads === 1 ? Response.json({ data: [subscription], next_cursor: null }) : pending
      },
    },
    {
      method: 'DELETE',
      path: `${path}/${subscription.id}`,
      respond: () => {
        deleted = true
        return new Response(null, { status: 204 })
      },
    },
  ])
  const { cache } = render(api)
  await waitForUI(() => {
    expect(button(stopLabel)).toBeDefined()
  })
  let refresh!: Promise<void>
  act(() => {
    refresh = cache.invalidateQueries()
  })
  await waitForUI(() => {
    expect(reads).toBe(2)
  })
  vi.stubGlobal('confirm', () => true)
  act(() => {
    button(stopLabel).click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Could not load conversations.')
  })
  await act(async () => {
    release(Response.json({ data: [subscription], next_cursor: null }))
    await refresh
  })
  expect(container.textContent).not.toContain('Support agent')
  expect(container.querySelectorAll('li')).toHaveLength(0)
})

function Attach() {
  const create = useCreateIntegrationSubscription(orgId, projectId, integration.id)
  return (
    <button
      onClick={() => {
        create.mutate({
          agent_id: subscription.agent_id,
          type: subscription.type,
          conversation: subscription.conversation,
        })
      }}
    >
      Attach via hook
    </button>
  )
}

it('refreshes the integration list after an explicit API attachment without changing config', async () => {
  let attached = false
  const api = fakeApi([
    {
      method: 'GET',
      path,
      respond: () => Response.json({ data: attached ? [subscription] : [], next_cursor: null }),
    },
    {
      method: 'POST',
      path,
      respond: ({ body }) => {
        expect(body).toEqual({
          agent_id: subscription.agent_id,
          type: subscription.type,
          conversation: subscription.conversation,
        })
        attached = true
        return Response.json(subscription, { status: 201 })
      },
    },
  ])
  render(
    api,
    <>
      {conversations()}
      <Attach />
    </>,
  )
  await waitForUI(() => {
    expect(container.textContent).toContain('No conversations yet.')
  })
  act(() => {
    button('Attach via hook').click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Support agent')
  })
  expect(api.requests.every((request) => request.url.pathname === path)).toBe(true)
})
