/** @vitest-environment happy-dom */

import type { Agent, AgentProfile, EventWebhook, EventWebhookScope } from '@omnara/sdk'
import { zCreateEventWebhookRequest, zUpdateEventWebhookRequest } from '@omnara/sdk/zod'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { fakeApi, type RecordedRequest } from '@/test/fake-api'
import { agentConfigModel, fakeId } from '@/test/fixtures'
import { renderIntegration } from '@/test/integration-render'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, choose, enter, field, waitForUI } from '@/test/secret-editor'

import { EventWebhooksSection } from './EventWebhooksSection'

const orgId = fakeId('org'),
  projectId = fakeId('proj'),
  profileId = fakeId('aprf')
const projectPath = '/api/v1/orgs/' + orgId + '/projects/' + projectId
const path = projectPath + '/event-webhooks'
const agentScope: Extract<EventWebhookScope, { type: 'agent' }> = {
  type: 'agent',
  agent_id: fakeId('agt'),
}
const profileScope: Extract<EventWebhookScope, { type: 'profile' }> = {
  type: 'profile',
  agent_profile_id: profileId,
}
const projectScope: EventWebhookScope = { type: 'project' }
const now = '2026-10-23T00:00:00Z'
const agent: Agent = {
  id: agentScope.agent_id,
  org_id: orgId,
  project_id: projectId,
  state: 'archived',
  name: 'Test agent',
  created_at: now,
  updated_at: now,
}
const profile: AgentProfile = {
  id: profileId,
  org_id: orgId,
  project_id: projectId,
  name: 'Support',
  current_generation: 1,
  current_config_id: fakeId('acfg'),
  created_at: now,
  updated_at: now,
  current_config: {
    id: fakeId('acfg'),
    org_id: orgId,
    project_id: projectId,
    model: agentConfigModel(),
    effective_definition_hash: 'hash',
    created_at: now,
    compiled_definition: { instruction: 'Help', model: { configured_model_id: fakeId('mdl') } },
  },
}
function webhook(scope: EventWebhookScope = projectScope, letter = 'a'): EventWebhook {
  return {
    id: 'whk_' + letter.repeat(26),
    org_id: orgId,
    project_id: projectId,
    scope,
    url: 'https://example.com/' + scope.type,
    event_types: ['tool_call_update'],
    created_at: now,
    updated_at: now,
  }
}

let root: Root, container: HTMLDivElement, restore: () => void
let rendered: ReturnType<typeof renderIntegration>
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
  rendered.cache.clear()
  container.remove()
  restore()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function mount(initial: EventWebhook[] = [], canManage = true) {
  let rows = initial
  let failRead = false,
    failWrite = false
  const api = fakeApi([
    {
      method: 'GET',
      path,
      respond: ({ url }) => {
        if (failRead)
          return Response.json({ code: 'internal_error', error: 'Unavailable' }, { status: 500 })
        const cursor = url.searchParams.get('cursor')
        const start = cursor ? rows.findIndex((row) => row.id === cursor) + 1 : 0
        const data = rows.slice(start, start + 2)
        return Response.json({
          data,
          next_cursor: start + 2 < rows.length ? data.at(-1)?.id : null,
        })
      },
    },
    {
      method: 'GET',
      path: projectPath + '/secrets',
      respond: () => Response.json({ data: [], next_cursor: null }),
    },
    {
      method: 'GET',
      path: projectPath + '/agent-profiles',
      respond: () => Response.json({ data: [profile], next_cursor: null }),
    },
    {
      method: 'GET',
      path: projectPath + '/agent-profiles/' + profileId,
      respond: () => Response.json(profile),
    },
    {
      method: 'GET',
      path: projectPath + '/agents',
      respond: () => Response.json({ data: [agent], next_cursor: null }),
    },
    {
      method: 'GET',
      path: projectPath + '/agents/' + agent.id,
      respond: () => Response.json({ agent, machine_ids: [], mcp_connections: [] }),
    },
    {
      method: 'POST',
      path,
      respond: ({ body }) => {
        if (failWrite)
          return Response.json(
            { code: 'conflict', error: 'Webhook already exists' },
            { status: 409 },
          )
        const created = {
          ...webhook(projectScope, String.fromCharCode(97 + rows.length)),
          ...zCreateEventWebhookRequest.parse(body),
        }
        rows = [...rows, created]
        return Response.json(created, { status: 201 })
      },
    },
    ...['a', 'b', 'c'].flatMap((letter) => {
      const id = 'whk_' + letter.repeat(26)
      return [
        {
          method: 'PATCH',
          path: path + '/' + id,
          respond: ({ body }: RecordedRequest) => {
            const { signing_secret_id, ...patch } = zUpdateEventWebhookRequest.parse(body)
            const current = rows.find((row) => row.id === id)
            if (!current) return new Response(null, { status: 404 })
            const updated = { ...current, ...patch }
            if (signing_secret_id !== undefined)
              updated.signing_secret_id = signing_secret_id ?? undefined
            rows = rows.map((row) => (row.id === id ? updated : row))
            return Response.json(updated)
          },
        },
        {
          method: 'DELETE',
          path: path + '/' + id,
          respond: () => {
            if (failWrite)
              return Response.json(
                { code: 'conflict', error: 'Could not delete webhook' },
                { status: 409 },
              )
            rows = rows.filter((row) => row.id !== id)
            return new Response(null, { status: 204 })
          },
        },
      ]
    }),
  ])
  rendered = renderIntegration(
    root,
    api,
    <EventWebhooksSection orgId={orgId} projectId={projectId} canManage={canManage} />,
  )
  return {
    api,
    failRead: (value: boolean) => {
      failRead = value
    },
    failWrite: (value: boolean) => {
      failWrite = value
    },
  }
}

it('creates all selections from one form, pages through the project, and edits a selection in place', async () => {
  const { api } = mount()
  for (const scope of [projectScope, profileScope, agentScope]) {
    act(() => {
      button('Add webhook').click()
    })
    expect(button('Create webhook').disabled).toBe(true)
    await enter('Webhook URL', 'https://example.com/' + scope.type)
    if (scope.type === 'profile') {
      await choose('Receive events from', 'Agents using one profile')
      expect(button('Create webhook').disabled).toBe(true)
      await choose('Agent profile', 'Support')
    }
    if (scope.type === 'agent') {
      await choose('Receive events from', 'One agent')
      expect(button('Create webhook').disabled).toBe(true)
      await choose('Agent', 'Test agent')
    }
    act(() => {
      button('Create webhook').click()
    })
    await waitForUI(() => {
      expect(document.querySelector('[role="dialog"]')).toBeNull()
    })
  }
  await waitForUI(() => {
    expect(container.querySelectorAll('li')).toHaveLength(2)
  })
  act(() => {
    button('Load more webhooks').click()
  })
  await waitForUI(() => {
    expect(container.querySelectorAll('li')).toHaveLength(3)
    expect(container.textContent).toContain('Agents using profile: Support')
    expect(container.textContent).toContain('Agent: Test agent')
  })
  expect(
    api.requestsTo('GET', path).every((request) => !request.url.searchParams.has('scope_type')),
  ).toBe(true)
  expect(
    api.requestsTo('GET', path).some((request) => request.url.searchParams.has('cursor')),
  ).toBe(true)
  expect(api.requestsTo('POST', path).map((request) => request.body)).toEqual(
    [projectScope, profileScope, agentScope].map((scope) => ({
      scope,
      url: 'https://example.com/' + scope.type,
      event_types: ['tool_call_update'],
    })),
  )
  act(() => {
    button('Edit webhook https://example.com/agent').click()
  })
  await choose('Receive events from', 'Agents using one profile')
  await choose('Agent profile', 'Support')
  act(() => {
    button('Save changes').click()
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="dialog"]')).toBeNull()
  })
  expect(api.requestsTo('PATCH', path + '/whk_' + 'c'.repeat(26))[0]?.body).toEqual({
    scope: profileScope,
    url: 'https://example.com/agent',
    event_types: ['tool_call_update'],
    signing_secret_id: null,
  })
  await waitForUI(() => {
    const row = [...container.querySelectorAll('li')].find((item) =>
      item.textContent.includes('https://example.com/agent'),
    )
    expect(row?.textContent).toContain('Agents using profile: Support')
  })
})

it('shows all selections as editable and does not merge matching destination URLs', async () => {
  const rows = [webhook(), webhook(profileScope, 'b'), webhook(agentScope, 'c')].map((row) => ({
    ...row,
    url: 'https://example.com/events',
  }))
  mount(rows)
  await waitForUI(() => {
    expect(container.querySelectorAll('li')).toHaveLength(2)
  })
  act(() => {
    button('Load more webhooks').click()
  })
  await waitForUI(() => {
    expect(container.querySelectorAll('li')).toHaveLength(3)
  })
  expect(container.querySelectorAll('[aria-label^="Edit webhook"]')).toHaveLength(3)
  expect(container.querySelectorAll('[aria-label^="Delete webhook"]')).toHaveLength(3)
})

it('shows subscriptions without management controls to readers', async () => {
  const { api } = mount([webhook(), webhook(profileScope, 'b')], false)
  await waitForUI(() => {
    expect(container.querySelectorAll('li')).toHaveLength(2)
  })
  expect(container.querySelectorAll('button')).toHaveLength(0)
  expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
})

it('validates the URL and events, and cancels without writes', async () => {
  const { api } = mount()
  act(() => {
    button('Add webhook').click()
  })
  await enter('Webhook URL', 'http://example.com/events')
  expect(button('Create webhook').disabled).toBe(true)
  await enter('Webhook URL', 'https://example.com/events')
  act(() => {
    button('Remove Tool-call updates').click()
  })
  expect(button('Create webhook').disabled).toBe(true)
  act(() => {
    button('Cancel').click()
  })
  expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
})

it.each(['Agent input', 'All event types'])(
  'closes the event menu after selecting %s without closing the dialog',
  async (label) => {
    mount()
    act(() => {
      button('Add webhook').click()
    })
    act(() => {
      field('Event types').dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }),
      )
    })
    let option: HTMLElement | undefined
    await waitForUI(() => {
      option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(
        (item) => item.textContent === label,
      )
      expect(option).toBeDefined()
    })
    act(() => option?.click())
    await waitForUI(() => {
      expect(field('Event types').getAttribute('aria-expanded')).toBe('false')
      expect(document.querySelector('[role="dialog"]')).not.toBeNull()
    })
    expect(button('Remove Agent input')).toBeDefined()
  },
)

it('updates and deletes only the selected subscription', async () => {
  const { api } = mount([webhook(), webhook(profileScope, 'b')])
  await waitForUI(() => {
    expect(container.querySelectorAll('li')).toHaveLength(2)
  })
  act(() => {
    button('Edit webhook https://example.com/project').click()
  })
  await enter('Webhook URL', 'https://example.com/updated')
  act(() => {
    button('Save changes').click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('https://example.com/updated')
  })
  vi.stubGlobal(
    'confirm',
    vi.fn(() => false),
  )
  act(() => {
    button('Delete webhook https://example.com/updated').click()
  })
  expect(api.requestsTo('DELETE', path + '/whk_' + 'a'.repeat(26))).toHaveLength(0)
  vi.mocked(window.confirm).mockReturnValue(true)
  act(() => {
    button('Delete webhook https://example.com/updated').click()
  })
  await waitForUI(() => {
    expect(container.querySelectorAll('li')).toHaveLength(1)
  })
  expect(container.textContent).toContain('Agents using profile:')
  expect(
    api.requests.filter((request) => request.method !== 'GET').map((request) => request.method),
  ).toEqual(['PATCH', 'DELETE'])
})

it('keeps a rejected create editable without adopting another subscription', async () => {
  const fixture = mount()
  fixture.failWrite(true)
  act(() => {
    button('Add webhook').click()
  })
  await enter('Webhook URL', 'https://example.com/events')
  act(() => {
    button('Create webhook').click()
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain(
      'Webhook already exists',
    )
  })
  expect(field('Webhook URL').value).toBe('https://example.com/events')
  expect(
    fixture.api.requests
      .filter((request) => request.method !== 'GET')
      .map((request) => request.method),
  ).toEqual(['POST'])
})

it('keeps loaded rows visible on list failure and supports retry', async () => {
  const fixture = mount([webhook()])
  await waitForUI(() => {
    expect(container.querySelectorAll('li')).toHaveLength(1)
  })
  fixture.failRead(true)
  await rendered.cache.invalidateQueries()
  await waitForUI(() => {
    expect(container.textContent).toContain('Could not load all webhooks.')
  })
  expect(container.querySelectorAll('li')).toHaveLength(1)
  expect(container.textContent).not.toContain('No webhooks.')
  fixture.failRead(false)
  act(() => {
    button('Retry').click()
  })
  await waitForUI(() => {
    expect(container.querySelector('[role="alert"]')).toBeNull()
  })
})

it('keeps the row and displays failed deletion', async () => {
  const fixture = mount([webhook()])
  await waitForUI(() => {
    expect(container.querySelectorAll('li')).toHaveLength(1)
  })
  fixture.failWrite(true)
  vi.stubGlobal(
    'confirm',
    vi.fn(() => true),
  )
  act(() => {
    button('Delete webhook https://example.com/project').click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Could not delete webhook')
  })
  expect(container.querySelectorAll('li')).toHaveLength(1)
})
