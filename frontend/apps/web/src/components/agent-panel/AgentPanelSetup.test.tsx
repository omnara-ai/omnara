/** @vitest-environment happy-dom */

import { OmnaraClientProvider, useAgentPanel } from '@omnara/react'
import {
  type Agent,
  type AgentPanel,
  createOmnaraClient,
  schemas,
  type VisibleProject,
} from '@omnara/sdk'
import { getAgentPanelQueryKey, getCurrentUserQueryKey } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { fakeApi, type FakeRoute, jsonResponse } from '@/test/fake-api'
import { currentUser, currentUserOrg, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

import { AgentPanelSetup } from './AgentPanelSetup'

const owner = currentUserOrg({ id: fakeId('org'), name: 'Owned', role: 'owner' })
const admin = currentUserOrg({ id: `org_${'b'.repeat(26)}`, name: 'Admin only', role: 'admin' })
const timestamp = '2026-01-01T00:00:00Z'
const first: VisibleProject = {
  id: fakeId('proj'),
  org_id: owner.id,
  name: 'Default',
  created_at: timestamp,
  updated_at: timestamp,
  access: { can_read: true, can_manage: true, can_manage_access: true, can_operate: true },
}
const second: VisibleProject = { ...first, id: `proj_${'b'.repeat(26)}`, name: 'Second' }
const projects = [first, second]
const createPath = `/api/v1/orgs/${owner.id}/assistant-agents`
let container: HTMLDivElement
let root: Root
let cache: QueryClient
let restore: () => void
let panel: AgentPanel

beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
  panel = { state: 'empty', enabled: true, revision: 0 }
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  cache.clear()
  restore()
})

async function render(routes: FakeRoute[], settings = false) {
  const api = fakeApi([
    ...routes,
    { method: 'GET', path: '/api/v1/me/agent-panel', respond: () => jsonResponse(panel) },
    {
      method: 'GET',
      path: `/api/v1/orgs/${owner.id}/projects`,
      respond: () => jsonResponse({ data: projects, next_cursor: null }),
    },
    {
      method: 'GET',
      path: `/api/v1/orgs/${owner.id}/projects/${first.id}/model-grants`,
      respond: () => jsonResponse({ data: [], next_cursor: null }),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', maxRetries: 0 })
  client.setConfig({ fetch: api.fetch })
  cache = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  cache.setQueryData(getCurrentUserQueryKey({ client }), currentUser([owner, admin]))
  cache.setQueryData(getAgentPanelQueryKey({ client }), panel)
  const connected = vi.fn()
  const closed = vi.fn()
  function Harness() {
    const query = useAgentPanel()
    return query.data == null ? null : (
      <AgentPanelSetup
        panel={query.data}
        onConnected={connected}
        onClose={settings ? closed : undefined}
      />
    )
  }
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={cache}>
          <Harness />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  return { api, connected, closed }
}

async function click(element: Element | null | undefined) {
  if (!(element instanceof HTMLElement)) throw new Error('Missing clickable element')
  await act(async () => {
    element.click()
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

function button(label: string, last = false) {
  const matches = [...container.querySelectorAll('button')].filter(
    (element) => element.textContent.trim() === label,
  )
  return last ? matches.at(-1) : matches[0]
}

async function choose(id: string, name: string) {
  await click(document.getElementById(id))
  await waitForUI(() => {
    expect(
      [...document.querySelectorAll('[role="option"]')].some(
        (item) => item.textContent.trim() === name,
      ),
    ).toBe(true)
  })
  await click(
    [...document.querySelectorAll('[role="option"]')].find(
      (item) => item.textContent.trim() === name,
    ),
  )
}

it('offers only owned organizations for creation and recovers from conflicts and ambiguous replies with one request ID', async () => {
  let calls = 0
  const { api, connected } = await render([
    {
      method: 'POST',
      path: createPath,
      respond: () => {
        calls++
        if (calls === 1) {
          panel = { ...panel, revision: 1 }
          return jsonResponse({ code: 'conflict', error: 'Selection changed' }, 409)
        }
        if (calls === 2) {
          panel = { ...panel, revision: 2 }
          return jsonResponse({ code: 'service_unavailable', error: 'Reply lost' }, 503)
        }
        return jsonResponse({ agent_id: fakeId('agt'), panel })
      },
    },
  ])
  await click(button('Create new'))
  await click(document.getElementById('panel-organization'))
  expect(
    [...document.querySelectorAll('[role="option"]')].map((item) => item.textContent.trim()),
  ).toEqual(['Owned'])
  await click([...document.querySelectorAll('[role="option"]')][0])
  await choose('panel-project', 'Default')
  await click(button('Create assistant', true))
  await waitForUI(() => {
    expect(container.textContent).toContain('Selection changed')
  })
  await click(button('Create assistant', true))
  await waitForUI(() => {
    expect(container.textContent).toContain('Reply lost')
  })
  await click(button('Create assistant', true))
  await waitForUI(() => {
    expect(connected).toHaveBeenCalledOnce()
  })
  const bodies = api
    .requestsTo('POST', createPath)
    .map((request) => schemas.zCreateAssistantAgentRequest.parse(request.body))
  expect(bodies.map(({ request_id: _requestID, ...body }) => body)).toEqual(
    [0, 1, 2].map((revision) => ({
      project_id: first.id,
      expected_revision: BigInt(revision),
      automatic: false,
    })),
  )
  const ids = bodies.map((body) => body.request_id)
  expect(new Set(ids).size).toBe(1)
})

it('never offers an agent from the previous project while the next project loads', async () => {
  let finish: (response: Response) => void = () => undefined
  const loading = new Promise<Response>((resolve) => {
    finish = resolve
  })
  const agent = {
    id: fakeId('agt'),
    org_id: owner.id,
    project_id: first.id,
    name: 'First agent',
    state: 'active',
    created_at: timestamp,
    updated_at: timestamp,
  } satisfies Agent
  await render([
    {
      method: 'GET',
      path: `/api/v1/orgs/${owner.id}/projects/${first.id}/agents`,
      respond: () => jsonResponse({ data: [agent], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `/api/v1/orgs/${owner.id}/projects/${second.id}/agents`,
      respond: () => loading,
    },
  ])
  await choose('panel-project', 'Default')
  await choose('panel-agent', 'First agent')
  await choose('panel-project', 'Second')
  await click(document.getElementById('panel-agent'))
  expect(
    [...document.querySelectorAll('[role="option"]')].map((item) => item.textContent.trim()),
  ).not.toContain('First agent')
  expect(button('Connect agent', true)?.disabled).toBe(true)
  act(() => {
    finish(
      jsonResponse({
        data: [{ ...agent, project_id: second.id, name: 'Second agent' }],
        next_cursor: null,
      }),
    )
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="option"]')?.textContent).toContain('Second agent')
  })
})

it('shares settings pending state and shows a failed update before allowing recovery', async () => {
  panel = {
    ...panel,
    state: 'ready',
    selection: {
      agent_id: fakeId('agt'),
      name: 'Connected agent',
      can_operate: true,
      scope: {
        org_id: owner.id,
        org_name: owner.name,
        project_id: first.id,
        project_name: first.name,
      },
    },
  }
  let finish!: (response: Response) => void
  const pending = new Promise<Response>((resolve) => {
    finish = resolve
  })
  let calls = 0
  const { api, closed } = await render(
    [
      {
        method: 'PATCH',
        path: '/api/v1/me/agent-panel',
        respond: () => {
          if (++calls === 1) return pending
          panel = { ...panel, enabled: false, revision: 1 }
          return jsonResponse(panel)
        },
      },
    ],
    true,
  )
  await click(button('Disconnect'))
  await waitForUI(() => {
    expect(button('Disconnect')?.disabled).toBe(true)
    expect(button('Turn off panel')?.disabled).toBe(true)
    expect(button('Create new')?.disabled).toBe(true)
  })
  await act(async () => {
    finish(jsonResponse({ code: 'service_unavailable', error: 'Try again' }, 503))
    await Promise.resolve()
  })
  await waitForUI(() => {
    expect(container.querySelector('[role="alert"]')?.textContent).toBe('Try again')
    expect(button('Turn off panel')?.disabled).toBe(false)
  })
  expect(closed).not.toHaveBeenCalled()
  await click(button('Turn off panel'))
  await waitForUI(() => {
    expect(closed).toHaveBeenCalledOnce()
  })
  expect(api.requestsTo('PATCH', '/api/v1/me/agent-panel').map(({ body }) => body)).toEqual([
    { agent_id: null },
    { enabled: false },
  ])
})
