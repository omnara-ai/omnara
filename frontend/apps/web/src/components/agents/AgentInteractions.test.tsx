/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { type AgentInteraction, createOmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, waitForUI } from '@/test/secret-editor'

import { AgentInteractions } from './AgentInteractions'

const orgId = fakeId('org')
const projectId = fakeId('proj')
const agentId = fakeId('agt')
const interaction = {
  id: fakeId('int'),
  org_id: orgId,
  project_id: projectId,
  agent_id: agentId,
  tool_call_id: fakeId('tcl'),
  tool_name: 'bash',
  interaction_kind: 'permission',
  state: 'open',
  request: {
    title: 'Allow bash?',
    questions: [
      {
        prompt: 'Allow this tool call?',
        options: [
          { label: 'Allow', allows_text: false },
          { label: 'Deny', allows_text: true },
        ],
      },
    ],
  },
  created_at: '2026-01-01T00:00:00Z',
} satisfies AgentInteraction
const agentPath = `/api/v1/orgs/${orgId}/projects/${projectId}/agents/${agentId}`
const resolvePath = `${agentPath}/interactions/${interaction.id}/resolve`

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

const authorization = {
  ...interaction,
  id: fakeId('int'),
  tool_name: 'mcp__calendar__create_event',
  interaction_kind: 'authorization',
  request: {
    title: 'calendar needs authorization',
    context: [{ label: 'Requested access', value: 'calendar.events' }],
    questions: [
      {
        prompt: 'Grant access to calendar so the agent can call create_event?',
        options: [{ label: 'Grant access' }, { label: 'Decline' }],
      },
    ],
  },
  authorization: {
    mcp_server_key: 'calendar',
    mcp_tool_name: 'create_event',
    mcp_url: 'https://calendar.example/mcp',
    secret_id: fakeId('sec'),
    secret_name: 'calendar-oauth',
    required_scopes: ['calendar.events'],
    granted_scopes: ['calendar.read'],
  },
} satisfies AgentInteraction
const authorizationPath = `${agentPath}/interactions/${authorization.id}`

async function render(
  current: typeof interaction | typeof authorization = interaction,
  ready = 'Allow',
) {
  const api = fakeApi([
    {
      method: 'GET',
      path: `${agentPath}/interactions`,
      respond: () => jsonResponse({ data: [current], next_cursor: null }),
    },
    {
      method: 'POST',
      path: `${agentPath}/interactions/${current.id}/resolve`,
      respond: () => jsonResponse({ ...current, state: 'resolved' }),
    },
    {
      method: 'POST',
      path: `${agentPath}/interactions/${current.id}/authorize`,
      respond: () =>
        jsonResponse(
          {
            flow_id: fakeId('moaf'),
            authorization_url: 'https://accounts.example/authorize?state=abc',
            expires_at: '2026-01-01T00:10:00Z',
          },
          201,
        ),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <AgentInteractions orgID={orgId} projectID={projectId} agentID={agentId} canOperate />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await Promise.resolve()
  })
  await waitForUI(() => {
    expect(button(ready)).toBeDefined()
  })
  return api
}

it('allows a permission request in one click', async () => {
  const api = await render()
  expect(container.textContent).not.toContain('Submit')
  await act(async () => {
    button('Allow').click()
    await Promise.resolve()
  })
  await waitForUI(() => {
    expect(api.requestsTo('POST', resolvePath).map((request) => request.body)).toEqual([
      { answers: [{ option_indices: [0] }] },
    ])
  })
})

it('denies with an optional reason', async () => {
  const api = await render()
  await act(async () => {
    button('Add a reason').click()
    await Promise.resolve()
  })
  await enter('Reason for denying (optional)', '  touches prod  ')
  await act(async () => {
    button('Deny').click()
    await Promise.resolve()
  })
  await waitForUI(() => {
    expect(api.requestsTo('POST', resolvePath).map((request) => request.body)).toEqual([
      { answers: [{ option_indices: [1], text: 'touches prod' }] },
    ])
  })
})

it('starts authorization and redirects to the consent screen', async () => {
  const assign = vi.spyOn(window.location, 'assign').mockImplementation(() => undefined)
  const api = await render(authorization, 'Grant access')
  expect(container.textContent).toContain('Authorization requested for')
  expect(container.textContent).toContain('calendar.events')
  await act(async () => {
    button('Grant access').click()
    await Promise.resolve()
  })
  await waitForUI(() => {
    expect(assign).toHaveBeenCalledWith('https://accounts.example/authorize?state=abc')
  })
  expect(api.requestsTo('POST', `${authorizationPath}/authorize`)).toHaveLength(1)
  expect(api.requestsTo('POST', `${authorizationPath}/resolve`)).toHaveLength(0)
  assign.mockRestore()
})

it('declines authorization through resolve', async () => {
  const api = await render(authorization, 'Grant access')
  await act(async () => {
    button('Decline').click()
    await Promise.resolve()
  })
  await waitForUI(() => {
    expect(
      api.requestsTo('POST', `${authorizationPath}/resolve`).map((request) => request.body),
    ).toEqual([{ answers: [{ option_indices: [1] }] }])
  })
})
