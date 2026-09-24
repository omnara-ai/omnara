/** @vitest-environment happy-dom */

import { getProjectIntegrationQueryKey } from '@omnara/sdk/tanstack'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { z } from 'zod'

import { ProjectIntegrationDetail } from '@/routes/ProjectIntegrationPage'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId, integrationDefinition, projectIntegration } from '@/test/fixtures'
import { renderProjectIntegration } from '@/test/project-integration-render'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, waitForUI } from '@/test/secret-editor'

import { RemoveProjectIntegrationButton } from './ProjectIntegrationActions'
import { ProjectIntegrationAdvanced } from './ProjectIntegrationAdvanced'
import { ProjectIntegrationsList } from './ProjectIntegrationsList'
import { useProjectIntegrationActions } from './useProjectIntegrationActions'

const orgId = fakeId('org'),
  projectId = fakeId('proj')
const projectPath = `/api/v1/orgs/${orgId}/projects/${projectId}`
const savedIntegration = projectIntegration({
  integration_type: 'github_pr',
  name: 'reviewer',
  state: 'active',
  provider_tenant_id: '111',
  provider_account_ref: '222',
  settings: {
    launcher: {
      trigger: 'pull_request_opened',
      scope_kind: 'repository',
      scope_ref: '123',
      slots: [{ key: 'default', agent_profile_id: fakeId('aprf') }],
    },
  },
})

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
  window.history.replaceState(null, '', '/')
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function render(api: ReturnType<typeof fakeApi>, content: ReactNode) {
  return renderProjectIntegration(root, api, content)
}

function RemoveIntegration({ onRemoved }: { onRemoved: () => void }) {
  const actions = useProjectIntegrationActions(orgId, projectId)
  return (
    <RemoveProjectIntegrationButton
      integration={savedIntegration}
      actions={actions}
      onRemoved={onRemoved}
    />
  )
}

async function selectAction(name: string) {
  act(() => {
    button('Integration actions').dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }),
    )
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="menuitem"]')).not.toBeNull()
  })
  const item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
    (element) => element.textContent.trim() === name,
  )
  if (!item) throw new Error(`Missing integration action: ${name}`)
  act(() => {
    item.click()
  })
}

it('lists integrations without a profile, retries a failed page and retains previously loaded integrations', async () => {
  let secondAttempts = 0
  const secondIntegration = {
    ...savedIntegration,
    id: `itg_${'b'.repeat(26)}`,
    name: 'second-setup',
  }
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/integrations`,
      respond: ({ url }) => {
        if (!url.searchParams.has('cursor'))
          return jsonResponse(z.json().parse({ data: [savedIntegration], next_cursor: 'page-two' }))
        secondAttempts++
        if (secondAttempts === 1)
          return jsonResponse({ code: 'internal_error', error: 'Temporary failure' }, 500)
        return jsonResponse(z.json().parse({ data: [secondIntegration], next_cursor: null }))
      },
    },
  ])
  render(api, <ProjectIntegrationsList orgId={orgId} projectId={projectId} canManage />)
  await waitForUI(() => {
    expect(document.body.textContent).toContain(savedIntegration.name)
  })
  expect(
    container.querySelector(`a[href="/projects/${projectId}/integrations/${savedIntegration.id}"]`),
  ).not.toBeNull()
  expect(
    container.querySelector(`a[href="/projects/${projectId}/integrations/new"]`)?.textContent,
  ).toBe('Add integration')
  act(() => {
    button('Load more integrations').click()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Could not load integrations.')
  })
  act(() => {
    button('Retry').click()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain(savedIntegration.name)
  })
  act(() => {
    button('Load more integrations').click()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain(secondIntegration.name)
  })
  expect(document.body.textContent).toContain(savedIntegration.name)
  expect(container.querySelectorAll('a')).toHaveLength(3)
  expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
})

it.each([true, false, undefined])(
  'offers the catalog for an empty project only to managers (canManage=%s)',
  async (canManage) => {
    const definitions = [
      integrationDefinition('slack_thread'),
      integrationDefinition('discord_thread'),
      integrationDefinition('github_pr'),
    ]
    const api = fakeApi([
      {
        method: 'GET',
        path: `${projectPath}/integrations`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: `${projectPath}/integration-definitions`,
        respond: () => Response.json({ data: definitions }),
      },
    ])
    render(
      api,
      <ProjectIntegrationsList orgId={orgId} projectId={projectId} canManage={canManage} />,
    )
    if (canManage) {
      await waitForUI(() => {
        expect(container.querySelectorAll('a')).toHaveLength(definitions.length)
      })
      expect([...container.querySelectorAll('a')].map((link) => link.getAttribute('href'))).toEqual(
        definitions.map(
          (definition) => `/projects/${projectId}/integrations/new/${definition.integration_type}`,
        ),
      )
    } else {
      await waitForUI(() => {
        expect(container.textContent).toContain('Ask a project administrator to add one.')
      })
      expect(container.querySelectorAll('a')).toHaveLength(0)
      expect(api.requestsTo('GET', `${projectPath}/integration-definitions`)).toHaveLength(0)
    }
  },
)

it.each([true, false])(
  'respects management access in integration details (canManage=%s)',
  async (canManage) => {
    let integration = savedIntegration
    let attempts = 0
    const api = fakeApi([
      {
        method: 'GET',
        path: `${projectPath}/integrations/${integration.id}/subscriptions`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: `${projectPath}/integrations/${integration.id}`,
        respond: () => jsonResponse(z.json().parse(integration)),
      },
      {
        method: 'POST',
        path: `${projectPath}/integrations/${integration.id}/disconnect`,
        respond: () => {
          attempts++
          if (attempts === 1)
            return jsonResponse({ code: 'internal_error', error: 'Try again shortly' }, 500)
          integration = {
            ...integration,
            state: 'disconnected',
            setup_revision: integration.setup_revision + 1,
            updated_at: '2026-09-19T00:01:00Z',
          }
          return jsonResponse(z.json().parse(integration))
        },
      },
    ])
    render(
      api,
      <ProjectIntegrationDetail
        orgId={orgId}
        projectId={projectId}
        integrationId={integration.id}
        canManage={canManage}
      />,
    )
    await waitForUI(() => {
      expect(container.querySelector('h1')?.textContent).toBe(integration.name)
    })
    expect(container.querySelector('[aria-label="Pull requests"]')).not.toBeNull()
    expect(container.querySelector('[aria-label="Conversations"]')).not.toBeNull()
    if (!canManage) {
      expect(container.querySelector('[aria-label="Integration actions"]')).toBeNull()
      expect(container.querySelector('form')).toBeNull()
      expect(() => button('Edit')).toThrow('Missing button')
      expect(api.requests.every((request) => request.method === 'GET')).toBe(true)
      return
    }
    await selectAction('Reconnect account')
    const tenant = container.querySelector<HTMLInputElement>('#provider-tenant')
    expect(tenant?.value).toBe(savedIntegration.provider_tenant_id)
    expect(tenant?.readOnly).toBe(true)
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    act(() => {
      button('Cancel').click()
    })
    expect(container.querySelector('form')).toBeNull()
    expect(button('Edit')).toBeDefined()
    const confirm = vi.fn(() => false)
    vi.stubGlobal('confirm', confirm)
    await selectAction('Disconnect integration')
    expect(confirm).toHaveBeenCalledWith(
      expect.stringContaining('Subscriptions, agents and history are kept.'),
    )
    expect(
      api.requestsTo('POST', `${projectPath}/integrations/${integration.id}/disconnect`),
    ).toHaveLength(0)
    confirm.mockReturnValue(true)
    await selectAction('Disconnect integration')
    await waitForUI(() => {
      expect(document.body.textContent).toContain('Try again shortly')
    })
    expect(button('Delete integration')).toBeDefined()
    await selectAction('Disconnect integration')
    await waitForUI(() => {
      expect(container.textContent).toContain('This integration is disconnected.')
    })
    expect(
      api.requestsTo('POST', `${projectPath}/integrations/${integration.id}/disconnect`),
    ).toHaveLength(2)
    confirm.mockReturnValue(false)
    act(() => {
      button('Delete integration').click()
    })
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('This deletes its schedules'))
    expect(api.requestsTo('DELETE', `${projectPath}/integrations/${integration.id}`)).toHaveLength(
      0,
    )
  },
)

it.each(['slack_thread', 'discord_thread', 'github_pr'] as const)(
  'shows usable %s capability keys from the integration',
  (integrationType) => {
    const integration = projectIntegration({
      integration_type: integrationType,
      name: 'customer-support',
    })
    render(fakeApi([]), <ProjectIntegrationAdvanced integration={integration} />)
    expect(button('Advanced').getAttribute('aria-expanded')).toBe('false')
    act(() => {
      button('Advanced').click()
    })
    const section = container.querySelector('[aria-label="Advanced"]')
    const keys = [...(section?.querySelectorAll('code') ?? [])].map((code) => code.textContent)
    expect(keys).toContain('int__customer-support__read')
    expect(section?.textContent).toContain('Subscription types')
    expect(keys).toContain(integrationType === 'github_pr' ? 'pull_request' : 'thread_messages')
    if (integrationType === 'github_pr') {
      expect(keys).not.toContain('interaction_handlers')
    } else {
      expect(keys).toContain('interaction_handlers')
      expect(keys).toContain('customer-support')
    }
  },
)

it.each(['active', 'disconnected'] as const)(
  'shows current connection failures only for an active integration and refreshes status (%s)',
  async (state) => {
    let failing = true
    const integration = projectIntegration({
      integration_type: 'discord_thread',
      state,
      provider_tenant_id: '111',
      provider_account_ref: '222',
    })
    const api = fakeApi([
      {
        method: 'GET',
        path: `${projectPath}/integrations/${integration.id}/subscriptions`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: `${projectPath}/integrations/${integration.id}`,
        respond: () =>
          Response.json({
            ...integration,
            runtime_failure: failing
              ? {
                  message: 'Discord Gateway closed: 4014',
                  retry_at: '2026-09-23T12:00:00Z',
                }
              : undefined,
          }),
      },
    ])
    render(
      api,
      <ProjectIntegrationDetail
        orgId={orgId}
        projectId={projectId}
        integrationId={integration.id}
        canManage={false}
      />,
    )
    await waitForUI(() => {
      expect(container.querySelector('h1')?.textContent).toBe(integration.name)
    })
    if (state === 'disconnected') {
      expect(container.textContent).not.toContain('Connection failed:')
      expect(container.textContent).toContain('not queued for replay')
      return
    }
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      'Discord Gateway closed: 4014',
    )
    expect(container.querySelector('time')?.dateTime).toBe('2026-09-23T12:00:00Z')
    failing = false
    act(() => {
      button('Refresh status').click()
    })
    await waitForUI(() => {
      expect(container.textContent).not.toContain('Connection failed:')
    })
    expect(api.requestsTo('GET', `${projectPath}/integrations/${integration.id}`)).toHaveLength(2)
  },
)

it.each([true, false])(
  'keeps runtime failures while saving until GET reports the current failure (still failing: %s)',
  async (stillFailing) => {
    const failure = {
      message: 'Discord Gateway closed: 4014',
      retry_at: '2026-09-23T12:00:00Z',
    }
    const integration = projectIntegration({
      integration_type: 'discord_thread',
      state: 'active',
      provider_tenant_id: '111',
      provider_account_ref: '222',
    })
    const integrationPath = `${projectPath}/integrations/${integration.id}`
    const updated = { ...integration, updated_at: '2026-09-23T11:00:00Z' }
    let saved = false
    let release!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => {
      release = resolve
    })
    const api = fakeApi([
      {
        method: 'GET',
        path: integrationPath,
        respond: () =>
          saved ? pending : Response.json({ ...integration, runtime_failure: failure }),
      },
      {
        method: 'PUT',
        path: integrationPath,
        respond: () => {
          saved = true
          return Response.json(updated)
        },
      },
      ...['agent-profiles', 'cron-triggers', `integrations/${integration.id}/subscriptions`].map(
        (resource) => ({
          method: 'GET',
          path: `${projectPath}/${resource}`,
          respond: () => Response.json({ data: [], next_cursor: null }),
        }),
      ),
    ])
    const { cache, client } = render(
      api,
      <ProjectIntegrationDetail
        orgId={orgId}
        projectId={projectId}
        integrationId={integration.id}
        canManage
      />,
    )
    await waitForUI(() => {
      expect(container.textContent).toContain(`Connection failed: ${failure.message}`)
    })
    act(() => {
      button('Choose profiles').click()
    })
    act(() => {
      button('Save changes').click()
    })
    await waitForUI(() => {
      expect(api.requestsTo('PUT', integrationPath)).toHaveLength(1)
      expect(api.requestsTo('GET', integrationPath)).toHaveLength(2)
    })
    const queryKey = getProjectIntegrationQueryKey({
      path: { orgID: orgId, projectID: projectId, integrationID: integration.id },
      client,
    })
    expect(cache.getQueryData(queryKey)).toMatchObject({ runtime_failure: failure })
    expect(container.textContent).toContain(`Connection failed: ${failure.message}`)
    act(() => {
      release(Response.json({ ...updated, runtime_failure: stillFailing ? failure : undefined }))
    })
    await waitForUI(() => {
      expect(button('Choose profiles')).toBeDefined()
      expect(cache.getQueryData(queryKey)).toEqual({
        ...updated,
        runtime_failure: stillFailing ? failure : undefined,
      })
      expect(container.textContent.includes(`Connection failed: ${failure.message}`)).toBe(
        stillFailing,
      )
    })
  },
)

it('keeps an edit draft mounted through a failed background refresh', async () => {
  let unavailable = false
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/integrations/${savedIntegration.id}/subscriptions`,
      respond: () => Response.json({ data: [], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `${projectPath}/integrations/${savedIntegration.id}`,
      respond: () =>
        unavailable
          ? jsonResponse({ code: 'internal_error', error: 'Temporary failure' }, 500)
          : jsonResponse(z.json().parse(savedIntegration)),
    },
  ])
  const { cache } = render(
    api,
    <ProjectIntegrationDetail
      orgId={orgId}
      projectId={projectId}
      integrationId={savedIntegration.id}
      canManage
    />,
  )
  await waitForUI(() => {
    expect(button('Edit')).toBeDefined()
  })
  act(() => {
    button('Edit').click()
  })
  act(() => {
    const select = container.querySelector<HTMLSelectElement>('#integration-trigger')
    if (!select) throw new Error('Missing trigger selector')
    select.value = 'mention'
    select.dispatchEvent(new Event('change', { bubbles: true }))
  })
  expect(container.querySelector('#launcher-scope')).toBeNull()
  expect(container.textContent).toContain('Restricted to repository 123')
  const draft = container.querySelector('form')
  unavailable = true
  await act(async () => {
    await cache.invalidateQueries()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain(
      'Could not refresh this integration. Your current edits are kept.',
    )
  })
  expect(container.querySelector<HTMLSelectElement>('#integration-trigger')?.value).toBe('mention')
  expect(container.querySelector('form')).toBe(draft)
  unavailable = false
  act(() => {
    button('Retry refresh').click()
  })
  await waitForUI(() => {
    expect(document.body.textContent).not.toContain('Could not refresh this integration.')
  })
  expect(container.querySelector<HTMLSelectElement>('#integration-trigger')?.value).toBe('mention')
})

it('does not let a delayed GET overwrite a successful integration update', async () => {
  let integration = savedIntegration
  let delay = false
  let release!: (response: Response) => void
  const pending = new Promise<Response>((resolve) => {
    release = resolve
  })
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/integrations/${integration.id}/subscriptions`,
      respond: () => Response.json({ data: [], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `${projectPath}/integrations/${integration.id}`,
      respond: () => (delay ? pending : jsonResponse(z.json().parse(integration))),
    },
    {
      method: 'POST',
      path: `${projectPath}/integrations/${integration.id}/disconnect`,
      respond: () => {
        integration = {
          ...integration,
          state: 'disconnected',
          setup_revision: integration.setup_revision + 1,
          updated_at: '2026-09-19T00:01:00Z',
        }
        return jsonResponse(z.json().parse(integration))
      },
    },
  ])
  const { cache, client } = render(
    api,
    <ProjectIntegrationDetail
      orgId={orgId}
      projectId={projectId}
      integrationId={integration.id}
      canManage
    />,
  )
  await waitForUI(() => {
    expect(button('Delete integration')).toBeDefined()
  })
  vi.stubGlobal('confirm', () => true)
  delay = true
  let refresh!: Promise<void>
  act(() => {
    refresh = cache.invalidateQueries()
  })
  await waitForUI(() => {
    expect(api.requestsTo('GET', `${projectPath}/integrations/${integration.id}`)).toHaveLength(2)
  })
  await selectAction('Disconnect integration')
  await waitForUI(() => {
    expect(container.textContent).toContain('This integration is disconnected.')
  })
  await act(async () => {
    release(jsonResponse(z.json().parse(savedIntegration)))
    await refresh
  })
  const queryKey = getProjectIntegrationQueryKey({
    path: { orgID: orgId, projectID: projectId, integrationID: integration.id },
    client,
  })
  expect(cache.getQueryData(queryKey)).toMatchObject({ state: 'disconnected' })
  expect(button('Reconnect account')).toBeDefined()
})

it('waits for disconnect to settle before removal and keeps the integration cache deleted', async () => {
  const detailPath = `${projectPath}/integrations/${savedIntegration.id}`
  let release!: (response: Response) => void
  const pending = new Promise<Response>((resolve) => {
    release = resolve
  })
  const api = fakeApi([
    { method: 'GET', path: detailPath, respond: () => Response.json(savedIntegration) },
    {
      method: 'GET',
      path: `${detailPath}/subscriptions`,
      respond: () => Response.json({ data: [], next_cursor: null }),
    },
    { method: 'POST', path: `${detailPath}/disconnect`, respond: () => pending },
    { method: 'DELETE', path: detailPath, respond: () => new Response(null, { status: 204 }) },
  ])
  const { cache, client, rerender } = render(
    api,
    <ProjectIntegrationDetail
      orgId={orgId}
      projectId={projectId}
      integrationId={savedIntegration.id}
      canManage
    />,
  )
  const queryKey = getProjectIntegrationQueryKey({
    path: { orgID: orgId, projectID: projectId, integrationID: savedIntegration.id },
    client,
  })
  await waitForUI(() => {
    expect(button('Delete integration').disabled).toBe(false)
  })
  vi.stubGlobal('confirm', () => true)
  await selectAction('Disconnect integration')
  await waitForUI(() => {
    expect(api.requestsTo('POST', `${detailPath}/disconnect`)).toHaveLength(1)
    expect(button('Delete integration').disabled).toBe(true)
    expect(button('Integration actions').disabled).toBe(true)
  })
  await act(async () => {
    button('Delete integration').click()
    await Promise.resolve()
  })
  expect(api.requestsTo('DELETE', detailPath)).toHaveLength(0)
  await act(async () => {
    release(
      Response.json({
        ...savedIntegration,
        state: 'disconnected',
        setup_revision: savedIntegration.setup_revision + 1,
      }),
    )
    await pending
  })
  await waitForUI(() => {
    expect(button('Delete integration').disabled).toBe(false)
    expect(cache.getQueryData(queryKey)).toMatchObject({ state: 'disconnected' })
  })
  const removed = vi.fn(() => {
    rerender(null)
  })
  rerender(<RemoveIntegration onRemoved={removed} />)
  act(() => {
    button('Delete integration').click()
  })
  await waitForUI(() => {
    expect(removed).toHaveBeenCalledOnce()
    expect(api.requestsTo('DELETE', detailPath)).toHaveLength(1)
    expect(cache.isMutating()).toBe(0)
    expect(cache.getQueryState(queryKey)).toBeUndefined()
  })
})

it('removes deleted integration details and does not restore them from a delayed read', async () => {
  let deleted = false
  let release!: (response: Response) => void
  const pending = new Promise<Response>((resolve) => {
    release = resolve
  })
  let reads = 0
  const api = fakeApi([
    {
      method: 'GET',
      path: `${projectPath}/integrations/${savedIntegration.id}/subscriptions`,
      respond: () => Response.json({ data: [], next_cursor: null }),
    },
    {
      method: 'GET',
      path: `${projectPath}/integrations/${savedIntegration.id}`,
      respond: () => {
        reads++
        if (deleted) return jsonResponse({ code: 'not_found', error: 'Integration removed' }, 404)
        return reads === 1 ? jsonResponse(z.json().parse(savedIntegration)) : pending
      },
    },
    {
      method: 'DELETE',
      path: `${projectPath}/integrations/${savedIntegration.id}`,
      respond: () => {
        deleted = true
        return new Response(null, { status: 204 })
      },
    },
  ])
  const detail = (
    <ProjectIntegrationDetail
      orgId={orgId}
      projectId={projectId}
      integrationId={savedIntegration.id}
      canManage
    />
  )
  const { cache, client, rerender } = render(api, detail)
  await waitForUI(() => {
    expect(button('Delete integration')).toBeDefined()
  })
  let refresh!: Promise<void>
  act(() => {
    refresh = cache.invalidateQueries()
  })
  await waitForUI(() => {
    expect(reads).toBe(2)
  })
  rerender(
    <RemoveIntegration
      onRemoved={() => {
        rerender(null)
      }}
    />,
  )
  vi.stubGlobal('confirm', () => true)
  act(() => {
    button('Delete integration').click()
  })
  const queryKey = getProjectIntegrationQueryKey({
    path: { orgID: orgId, projectID: projectId, integrationID: savedIntegration.id },
    client,
  })
  await waitForUI(() => {
    expect(deleted).toBe(true)
    expect(cache.getQueryState(queryKey)).toBeUndefined()
  })
  await act(async () => {
    release(jsonResponse(z.json().parse(savedIntegration)))
    await refresh
  })
  expect(cache.getQueryState(queryKey)).toBeUndefined()
  rerender(detail)
  await waitForUI(() => {
    expect(container.textContent).toContain('Could not load this integration.')
  })
  expect(reads).toBe(3)
  expect(container.querySelector('[aria-label="Pull requests"]')).toBeNull()
  expect(container.textContent).not.toContain('Delete integration')
})

it.each([401, 403, 404])(
  'stops showing integration settings after a %s refresh response',
  async (status) => {
    let unavailable = false
    const api = fakeApi([
      {
        method: 'GET',
        path: `${projectPath}/integrations/${savedIntegration.id}/subscriptions`,
        respond: () => Response.json({ data: [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: `${projectPath}/integrations/${savedIntegration.id}`,
        respond: () =>
          unavailable
            ? jsonResponse({ code: 'unavailable', error: 'Unavailable' }, status)
            : jsonResponse(z.json().parse(savedIntegration)),
      },
    ])
    const { cache } = render(
      api,
      <ProjectIntegrationDetail
        orgId={orgId}
        projectId={projectId}
        integrationId={savedIntegration.id}
        canManage
      />,
    )
    await waitForUI(() => {
      expect(button('Edit')).toBeDefined()
    })
    unavailable = true
    await act(async () => {
      await cache.invalidateQueries()
    })
    await waitForUI(() => {
      expect(container.textContent).toContain('Could not load this integration.')
    })
    expect(container.querySelector('[aria-label="Pull requests"]')).toBeNull()
  },
)
