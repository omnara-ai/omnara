/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { type AgentInteraction, createOmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, field, waitForUI } from '@/test/secret-editor'

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

async function render() {
  const api = fakeApi([
    {
      method: 'GET',
      path: `${agentPath}/interactions`,
      respond: () => jsonResponse({ data: [interaction], next_cursor: null }),
    },
    {
      method: 'POST',
      path: resolvePath,
      respond: () => jsonResponse({ ...interaction, state: 'resolved' }),
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
    expect(button('Allow')).toBeDefined()
  })
  return api
}

async function pressEnter(label: string, keyCode: number) {
  const event = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
  Object.defineProperty(event, 'keyCode', { value: keyCode })
  await act(async () => {
    field(label).dispatchEvent(event)
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
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

it('denies on Enter, but not on the Enter that confirms an IME candidate', async () => {
  const label = 'Reason for denying (optional)'
  const api = await render()
  await act(async () => {
    button('Add a reason').click()
    await Promise.resolve()
  })
  await enter(label, '本番に触る')
  await pressEnter(label, 229)
  expect(api.requestsTo('POST', resolvePath)).toEqual([])
  await pressEnter(label, 13)
  await waitForUI(() => {
    expect(api.requestsTo('POST', resolvePath).map((request) => request.body)).toEqual([
      { answers: [{ option_indices: [1], text: '本番に触る' }] },
    ])
  })
})
