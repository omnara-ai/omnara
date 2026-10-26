/** @vitest-environment happy-dom */

import type { EventWebhook, EventWebhookScope } from '@omnara/sdk'
import { zCreateEventWebhookRequest, zUpdateEventWebhookRequest } from '@omnara/sdk/zod'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { fakeApi } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { renderIntegration } from '@/test/integration-render'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, field, waitForUI } from '@/test/secret-editor'

import { EventWebhooksSection } from './EventWebhooksSection'

const orgId = fakeId('org'),
  projectId = fakeId('proj'),
  profileId = fakeId('aprf')
const path = '/api/v1/orgs/' + orgId + '/projects/' + projectId + '/event-webhooks'
const agentScope: Extract<EventWebhookScope, { kind: 'agent' }> = {
  kind: 'agent',
  agent_id: fakeId('agt'),
}
const profileScope: Extract<EventWebhookScope, { kind: 'profile' }> = {
  kind: 'profile',
  agent_profile_id: profileId,
}
const projectScope: EventWebhookScope = { kind: 'project' }
const now = '2026-10-23T00:00:00Z'
function webhook(scope: EventWebhookScope = agentScope, id = fakeId('whk')): EventWebhook {
  return {
    id,
    org_id: orgId,
    project_id: projectId,
    scope,
    url: 'https://example.com/events',
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

function mount(
  initial: EventWebhook[] = [],
  scope: EventWebhookScope = agentScope,
  inheritedProfile: string | null = profileId,
  canManage = true,
  sidebar = false,
) {
  let rows = initial
  let failRead: boolean | EventWebhookScope['kind'] = false
  let failWrite = false
  const api = fakeApi([
    {
      method: 'GET',
      path,
      respond: ({ url }) =>
        failRead === true || failRead === url.searchParams.get('scope_kind')
          ? Response.json({ code: 'internal_error', error: 'Unavailable' }, { status: 500 })
          : Response.json({
              data: rows.filter(
                (row) =>
                  row.scope.kind === url.searchParams.get('scope_kind') &&
                  (row.scope.kind === 'agent' ? row.scope.agent_id : null) ===
                    url.searchParams.get('agent_id') &&
                  (row.scope.kind === 'profile' ? row.scope.agent_profile_id : null) ===
                    url.searchParams.get('agent_profile_id'),
              ),
            }),
    },
    {
      method: 'GET',
      path: '/api/v1/orgs/' + orgId + '/projects/' + projectId + '/secrets',
      respond: () => Response.json({ data: [], next_cursor: null }),
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
        const created = { ...webhook(), ...zCreateEventWebhookRequest.parse(body) }
        rows = [...rows, created]
        return Response.json(created, { status: 201 })
      },
    },
    {
      method: 'PATCH',
      path: path + '/' + fakeId('whk'),
      respond: ({ body }) => {
        const { signing_secret_id, ...patch } = zUpdateEventWebhookRequest.parse(body)
        const current = rows.find((row) => row.id === fakeId('whk'))
        if (!current) return new Response(null, { status: 404 })
        const updated = { ...current, ...patch }
        if (signing_secret_id !== undefined)
          updated.signing_secret_id = signing_secret_id ?? undefined
        rows = rows.map((row) => (row.id === updated.id ? updated : row))
        return Response.json(updated)
      },
    },
    {
      method: 'DELETE',
      path: path + '/' + fakeId('whk'),
      respond: () => {
        if (failWrite)
          return Response.json(
            { code: 'conflict', error: 'Could not delete webhook' },
            { status: 409 },
          )
        rows = rows.filter((row) => row.id !== fakeId('whk'))
        return new Response(null, { status: 204 })
      },
    },
  ])
  rendered = renderIntegration(
    root,
    api,
    <EventWebhooksSection
      orgId={orgId}
      projectId={projectId}
      scope={scope}
      canManage={canManage}
      profileId={inheritedProfile ?? undefined}
      sidebar={sidebar}
    />,
  )
  return {
    api,
    failRead: (value: boolean | EventWebhookScope['kind']) => {
      failRead = value
    },
    failWrite: (value: boolean) => {
      failWrite = value
    },
  }
}

it('places the compact sidebar add button beside the heading and opens the create dialog', async () => {
  mount([], agentScope, profileId, true, true)
  await waitForUI(() => {
    expect(container.querySelector('p')?.textContent).toBe('No webhooks')
  })
  const add = button('Add webhook')
  expect(add.textContent.trim()).toBe('Add')
  expect(add.className).toContain('text-muted-foreground')
  expect(add.className).toContain('h-9')
  expect(add.className).toContain('sm:h-7')
  expect(add.className).toContain('px-2')
  expect(add.parentElement?.querySelector('[data-sidebar="group-label"]')?.textContent).toBe(
    'Webhooks',
  )
  act(() => {
    add.click()
  })
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain('Add webhook')
})

it('keeps the sidebar heading without an add button for readers', async () => {
  mount([], agentScope, profileId, false, true)
  await waitForUI(() => {
    expect(container.querySelector('p')?.textContent).toBe('No webhooks')
  })
  expect(container.querySelector('p')?.classList.contains('py-1.5')).toBe(true)
  expect(container.querySelector('p')?.classList.contains('truncate')).toBe(true)
  expect(container.querySelector('[data-sidebar="group-label"]')?.textContent).toBe('Webhooks')
  expect(container.querySelectorAll('button')).toHaveLength(0)
})

it.each([false, true])(
  'shows scoped rows without merging URLs or allowing inherited edits (sidebar: %s)',
  async (sidebar) => {
    const webhooks = [
      webhook(),
      webhook(profileScope, 'whk_' + 'b'.repeat(26)),
      webhook(projectScope, 'whk_' + 'c'.repeat(26)),
    ]
    const { api } = mount(webhooks, agentScope, profileId, true, sidebar)
    await waitForUI(() => {
      expect(container.querySelectorAll('li')).toHaveLength(3)
    })
    const rows = [...container.querySelectorAll('li')]
    expect(container.querySelectorAll('ul')).toHaveLength(1)
    expect(container.querySelectorAll('h3')).toHaveLength(0)
    expect(container.querySelector('ul')?.classList.contains('border')).toBe(sidebar)
    expect(rows.every((row) => row.classList.contains('px-3') === sidebar)).toBe(true)
    expect(rows.map((row) => row.querySelector('p:last-child')?.textContent)).toEqual([
      'Agent webhook',
      'Profile webhook',
      'Project webhook',
    ])
    expect(container.textContent).not.toContain('From ')
    expect(rows[0]?.querySelectorAll('button')).toHaveLength(2)
    expect(rows[1]?.querySelectorAll('button')).toHaveLength(0)
    expect(rows[2]?.querySelectorAll('button')).toHaveLength(0)
    expect(container.querySelectorAll('button')).toHaveLength(3)
    expect(container.querySelector('a')?.getAttribute('href')).toBe(
      '/projects/' + projectId + '/agent-profiles/' + profileId + '/webhooks',
    )
    expect(
      api
        .requestsTo('GET', path)
        .map((request) => request.url.searchParams.get('scope_kind'))
        .sort((a, b) => (a ?? '').localeCompare(b ?? '')),
    ).toEqual(['agent', 'profile', 'project'])
    expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
  },
)

it('shows profile webhooks as editable and project webhooks as inherited on the profile', async () => {
  const { api } = mount(
    [webhook(profileScope), webhook(projectScope, 'whk_' + 'b'.repeat(26))],
    profileScope,
  )
  await waitForUI(() => {
    expect(container.querySelectorAll('li')).toHaveLength(2)
  })
  expect(container.querySelectorAll('ul')).toHaveLength(1)
  expect(container.textContent).not.toContain('This profile')
  expect(container.textContent).toContain('Profile webhook')
  expect(container.textContent).toContain('Project webhook')
  expect(container.querySelectorAll('[aria-label^="Edit webhook"]')).toHaveLength(1)
  expect(
    api
      .requestsTo('GET', path)
      .map((request) => request.url.searchParams.get('scope_kind'))
      .sort((a, b) => (a ?? '').localeCompare(b ?? '')),
  ).toEqual(['profile', 'project'])
})

it('does not query a profile when the agent has none', async () => {
  const { api } = mount([], agentScope, null)
  await waitForUI(() => {
    expect(container.textContent).toContain('No webhooks.')
  })
  expect(
    api
      .requestsTo('GET', path)
      .map((request) => request.url.searchParams.get('scope_kind'))
      .sort((a, b) => (a ?? '').localeCompare(b ?? '')),
  ).toEqual(['agent', 'project'])
})

it.each([agentScope, profileScope])(
  'shows $kind webhooks without management controls to readers',
  async (scope) => {
    const { api } = mount(
      [webhook(scope), webhook(projectScope, 'whk_' + 'b'.repeat(26))],
      scope,
      null,
      false,
    )
    await waitForUI(() => {
      expect(container.querySelectorAll('li')).toHaveLength(2)
    })
    expect(container.querySelectorAll('button')).toHaveLength(0)
    expect(container.textContent).toContain('Project webhook')
    expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
  },
)

it.each([agentScope, profileScope])(
  'shows one plain empty state to $kind readers outside the sidebar',
  async (scope) => {
    mount([], scope, null, false)
    await waitForUI(() => {
      expect(container.textContent).toContain('No webhooks.')
    })
    expect(container.querySelectorAll('p')).toHaveLength(1)
    expect(container.querySelector('p')?.classList.contains('py-1.5')).toBe(false)
    expect(container.querySelector('p')?.classList.contains('truncate')).toBe(false)
    expect(container.querySelectorAll('button')).toHaveLength(0)
  },
)

it('does not show an empty state when only inherited webhooks exist', async () => {
  mount([webhook(profileScope), webhook(projectScope, 'whk_' + 'b'.repeat(26))])
  await waitForUI(() => {
    expect(container.querySelectorAll('li')).toHaveLength(2)
  })
  expect(container.textContent).not.toContain('No webhooks')
  expect(container.textContent).not.toContain('This agent')
  expect(container.querySelectorAll('[aria-label^="Edit webhook"]')).toHaveLength(0)
  expect(container.querySelectorAll('[aria-label^="Delete webhook"]')).toHaveLength(0)
  expect(container.querySelectorAll('button')).toHaveLength(1)
})

it('discards an open editor when navigating to another agent', async () => {
  const { api } = mount([webhook()])
  await waitForUI(() => {
    expect(container.querySelectorAll('li')).toHaveLength(1)
  })
  act(() => {
    button('Edit webhook https://example.com/events').click()
  })
  const scope = { ...agentScope, agent_id: 'agt_' + 'b'.repeat(26) }
  rendered.rerender(
    <EventWebhooksSection orgId={orgId} projectId={projectId} scope={scope} canManage />,
  )
  await waitForUI(() => {
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(container.textContent).toContain('No webhooks.')
  })
  expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
})

it.each([agentScope, profileScope])(
  'creates a webhook on the displayed $kind without touching config',
  async (scope) => {
    const { api } = mount([], scope)
    act(() => {
      button('Add webhook').click()
    })
    expect(button('Create webhook').disabled).toBe(true)
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
    act(() => {
      button('Add webhook').click()
    })
    await enter('Webhook URL', 'https://example.com/events')
    act(() => {
      button('Create webhook').click()
    })
    await waitForUI(() => {
      expect(document.querySelector('[role="dialog"]')).toBeNull()
      expect(container.querySelectorAll('li')).toHaveLength(1)
    })
    expect(
      api.requests
        .filter((request) => request.method !== 'GET')
        .map((request) => [request.method, request.url.pathname, request.body]),
    ).toEqual([
      [
        'POST',
        path,
        { scope, url: 'https://example.com/events', event_types: ['tool_call_update'] },
      ],
    ])
  },
)

it.each(['Agent input', 'All event types'])(
  'closes the event menu after selecting %s and allows reopening without closing the dialog',
  async (optionLabel) => {
    const { api } = mount()
    act(() => {
      button('Add webhook').click()
    })
    for (const label of [optionLabel, 'Model output']) {
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
      act(() => {
        option?.click()
      })
      await waitForUI(() => {
        expect(field('Event types').getAttribute('aria-expanded')).toBe('false')
        expect(document.querySelector('[role="listbox"]')).toBeNull()
        expect(document.querySelector('[role="dialog"]')).not.toBeNull()
      })
      expect(button('Remove Agent input')).toBeDefined()
      expect(button('Remove Tool-call updates')).toBeDefined()
    }
    expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
  },
)

it('cancels edits without writes, then updates and deletes only the selected webhook', async () => {
  const { api } = mount([webhook(), webhook(profileScope, 'whk_' + 'b'.repeat(26))])
  await waitForUI(() => {
    expect(container.querySelectorAll('li')).toHaveLength(2)
  })
  act(() => {
    button('Edit webhook https://example.com/events').click()
  })
  await enter('Webhook URL', 'https://example.com/unsaved')
  act(() => {
    button('Cancel').click()
  })
  expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
  act(() => {
    button('Edit webhook https://example.com/events').click()
  })
  expect(field('Webhook URL').value).toBe('https://example.com/events')
  await enter('Webhook URL', 'https://example.com/updated')
  act(() => {
    button('Save changes').click()
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(container.textContent).toContain('https://example.com/updated')
  })
  expect(api.requestsTo('PATCH', path + '/' + fakeId('whk'))[0]?.body).toEqual({
    url: 'https://example.com/updated',
    event_types: ['tool_call_update'],
    signing_secret_id: null,
  })
  vi.stubGlobal(
    'confirm',
    vi.fn(() => false),
  )
  act(() => {
    button('Delete webhook https://example.com/updated').click()
  })
  expect(api.requestsTo('DELETE', path + '/' + fakeId('whk'))).toHaveLength(0)
  vi.mocked(window.confirm).mockReturnValue(true)
  act(() => {
    button('Delete webhook https://example.com/updated').click()
  })
  await waitForUI(() => {
    expect(container.querySelectorAll('li')).toHaveLength(1)
  })
  expect(container.textContent).toContain('Profile webhook')
  expect(
    api.requests.filter((request) => request.method !== 'GET').map((request) => request.method),
  ).toEqual(['PATCH', 'DELETE'])
})

it('keeps a rejected create editable without adopting or updating an existing webhook', async () => {
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

it('shows list failures instead of an empty state and supports retry', async () => {
  const fixture = mount()
  fixture.failRead(true)
  await rendered.cache.invalidateQueries()
  await waitForUI(() => {
    expect(container.textContent).toContain('Could not load all webhooks.')
  })
  fixture.failRead(false)
  act(() => {
    button('Retry').click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('No webhooks.')
  })
})

it('keeps loaded rows visible and retries when an inherited scope fails', async () => {
  const fixture = mount([webhook(), webhook(projectScope, 'whk_' + 'b'.repeat(26))])
  fixture.failRead('profile')
  await rendered.cache.invalidateQueries()
  await waitForUI(() => {
    expect(container.querySelectorAll('li')).toHaveLength(2)
    expect(container.querySelectorAll('[role="alert"]')).toHaveLength(1)
  })
  expect(container.textContent).toContain('Could not load all webhooks.')
  expect(container.textContent).not.toContain('No webhooks')
  fixture.failRead(false)
  act(() => {
    button('Retry').click()
  })
  await waitForUI(() => {
    expect(container.querySelector('[role="alert"]')).toBeNull()
  })
  expect(container.querySelectorAll('li')).toHaveLength(2)
})

it('keeps the row and shows the error when deletion fails', async () => {
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
    button('Delete webhook https://example.com/events').click()
  })
  await waitForUI(() => {
    expect(container.textContent).toContain('Could not delete webhook')
  })
  expect(container.querySelectorAll('li')).toHaveLength(1)
})
