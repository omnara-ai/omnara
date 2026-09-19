/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { type AgentProfile, createOmnaraClient, type ProjectApp, schemas } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { type FakeApi, fakeApi, jsonResponse } from '@/test/fake-api'
import { agentConfigModel, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, waitForUI } from '@/test/secret-editor'

import { AgentProfileApps } from './AgentProfileApps'
import { ProjectAppProfilesDialog } from './ProjectAppProfilesDialog'
import { ProjectAppSetupDialog } from './ProjectAppSetupDialog'

const orgId = fakeId('org'),
  projectId = fakeId('proj'),
  connectionId = fakeId('iin')
const path = `/api/v1/orgs/${orgId}/projects/${projectId}`
const now = '2026-09-18T00:00:00Z'
function profile(letter: string, name: string): AgentProfile {
  return {
    id: `aprf_${letter.repeat(26)}`,
    name,
    org_id: orgId,
    project_id: projectId,
    current_generation: 1,
    current_config_id: fakeId('acfg'),
    current_config: {
      id: fakeId('acfg'),
      org_id: orgId,
      project_id: projectId,
      model: agentConfigModel(),
      effective_definition_hash: 'hash',
      created_at: now,
    },
    created_at: now,
    updated_at: now,
  }
}
const support = profile('a', 'Support'),
  reviews = profile('b', 'Reviews'),
  triage = profile('c', 'Triage')
const profileNameRoutes = [support, reviews, triage].map((item) => ({
  method: 'GET',
  path: path + '/agent-profiles/' + item.id,
  respond: () => Response.json(item),
}))
function connection(provider: 'slack' | 'discord', publicKey = '') {
  const providerConfig: Record<string, string> = {}
  if (publicKey) providerConfig.public_key = publicKey
  return {
    id: connectionId,
    org_id: orgId,
    project_id: projectId,
    provider,
    provider_tenant_id: provider === 'slack' ? 'T123' : '111',
    provider_account_ref: provider === 'slack' ? 'U123' : '222',
    provider_agent_display_name: 'Shared bot',
    state: 'active',
    provider_config: providerConfig,
    created_at: now,
    updated_at: now,
  }
}

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
})
function render(api: FakeApi, node: ReactNode) {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={cache}>{node}</QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
}
async function openProfiles() {
  await act(async () => {
    const input = document.querySelector<HTMLInputElement>('input[role="combobox"]')
    if (!input) throw new Error('Missing profile picker')
    input.focus()
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    await Promise.resolve()
  })
}
async function submit() {
  await act(async () => {
    const form = document.querySelector('form')
    if (!form) throw new Error('Missing app form')
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await Promise.resolve()
  })
}
async function chooseProfile(name: string) {
  await openProfiles()
  await waitForUI(() => {
    expect(
      [...document.querySelectorAll('[role="option"]')].some(
        (option) => option.textContent === name,
      ),
    ).toBe(true)
  })
  act(() => {
    const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(
      (item) => item.textContent === name,
    )
    if (!option) throw new Error(`Missing option: ${name}`)
    option.click()
  })
}
function selectConnection() {
  act(() => {
    const select = document.querySelector<HTMLSelectElement>('#app-connection')
    if (!select) throw new Error('Missing connection choice')
    select.value = connectionId
    select.dispatchEvent(new Event('change', { bubbles: true }))
  })
}

it.each(['slack', 'discord'] as const)(
  'creates a multiple-profile %s setup using a reusable bot and paginated profile choices',
  async (provider) => {
    const selected = connection(provider, 'ab'.repeat(32))
    const api = fakeApi([
      ...profileNameRoutes,
      {
        method: 'GET',
        path: path + '/integration-connections',
        respond: () => jsonResponse({ data: [selected], next_cursor: null }),
      },
      {
        method: 'GET',
        path: path + '/secrets',
        respond: () => jsonResponse({ data: [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: path + '/agent-profiles',
        respond: ({ url }) =>
          Response.json({
            data: url.searchParams.has('cursor') ? [reviews] : [support],
            next_cursor: url.searchParams.has('cursor') ? null : 'page-two',
          }),
      },
      {
        method: 'POST',
        path: path + '/apps',
        respond: ({ body }) =>
          Response.json(
            {
              ...schemas.zSaveProjectAppRequest.parse(body),
              id: fakeId('app'),
              project_id: projectId,
              created_at: now,
              updated_at: now,
            },
            { status: 201 },
          ),
      },
    ])
    const closed = vi.fn()
    render(
      api,
      <ProjectAppSetupDialog
        open
        onOpenChange={closed}
        orgId={orgId}
        projectId={projectId}
        profile={support}
      />,
    )
    if (provider === 'discord')
      act(() => {
        button('Discord').click()
      })
    await waitForUI(() => {
      expect(document.querySelector('#app-connection')?.textContent).toContain('Shared bot')
    })
    selectConnection()
    if (provider === 'discord') await enter('Channel ID', '333')
    await openProfiles()
    await waitForUI(() => {
      expect(button('Load more results')).toBeDefined()
    })
    act(() => {
      button('Load more results').click()
    })
    await chooseProfile('Reviews')
    expect(document.body.textContent).toContain('choose just one')
    expect(document.body.textContent).toContain('2/16 selected')
    if (provider === 'discord') {
      expect(document.body.textContent).toContain(
        `/api/integrations/discord/${connectionId}/interactions`,
      )
      expect(document.querySelector<HTMLInputElement>('input[name="interactions"]')?.checked).toBe(
        false,
      )
    }
    await submit()
    await waitForUI(() => {
      expect(closed).toHaveBeenCalledWith(false)
    })
    const saved = schemas.zSaveProjectAppRequest.parse(
      api.requestsTo('POST', path + '/apps')[0]?.body,
    )
    expect(saved.settings.launcher?.slots).toEqual([
      { key: 'default', agent_profile_id: support.id },
      { key: 'profile_2', agent_profile_id: reviews.id },
    ])
    expect(saved.settings.resource.connection).toBe(connectionId)
    expect(api.requests.filter((request) => request.method !== 'GET')).toHaveLength(1)
    expect(
      api
        .requestsTo('GET', path + '/agent-profiles')
        .some((request) => request.url.searchParams.get('cursor') === 'page-two'),
    ).toBe(true)
  },
)

const mixedApp: ProjectApp = {
  id: fakeId('app'),
  project_id: projectId,
  name: 'Shared support',
  enabled: false,
  created_at: now,
  updated_at: now,
  settings: {
    resource: {
      definition: 'omnara.slack',
      connection: connectionId,
      enabled: true,
      tools: { slack_read: { deferred: true, permission: { mode: 'always_allow' } } },
      listener: { events: ['message'] },
      interaction_handler: { definition: 'omnara.slack.interactions' },
      scope: { slack: { channel_id: 'C123', thread_ts: '123.456' } },
    },
    launcher: {
      trigger: 'mention',
      scope_kind: 'channel',
      scope_ref: 'C123',
      slots: [
        { key: 'original', agent_profile_id: support.id },
        { key: 'off-page', agent_profile_id: reviews.id },
        { key: 'profile_1', agent_id: fakeId('agt') },
      ],
    },
  },
}

it('edits profiles from the saved app list, keeps off-page and existing-agent slots, and retries a failed save', async () => {
  let attempts = 0
  const selected = connection('slack')
  const api = fakeApi([
    ...profileNameRoutes,
    {
      method: 'GET',
      path: path + '/apps',
      respond: () => Response.json({ data: [mixedApp], next_cursor: null }),
    },
    {
      method: 'GET',
      path: path + '/integration-connections',
      respond: () => jsonResponse({ data: [selected], next_cursor: null }),
    },
    {
      method: 'GET',
      path: path + '/integration-connections/' + connectionId,
      respond: () => jsonResponse(selected),
    },
    {
      method: 'GET',
      path: path + '/agent-profiles',
      respond: ({ url }) =>
        Response.json({
          data: url.searchParams.has('name') ? [triage] : [support],
          next_cursor: null,
        }),
    },
    {
      method: 'PUT',
      path: path + '/apps/' + mixedApp.id,
      respond: ({ body }) => {
        attempts++
        if (attempts === 1)
          return jsonResponse({ code: 'conflict', error: 'Try saving again' }, 409)
        return Response.json({ ...mixedApp, ...schemas.zSaveProjectAppRequest.parse(body) })
      },
    },
  ])
  render(
    api,
    <AgentProfileApps orgId={orgId} projectId={projectId} profileId={support.id} canManage />,
  )
  await waitForUI(() => {
    expect(button('Edit profiles')).toBeDefined()
  })
  act(() => {
    button('Edit profiles').click()
  })
  await waitForUI(() => {
    expect(button('Remove Support')).toBeDefined()
    expect(button('Remove Reviews')).toBeDefined()
  })
  expect(api.requestsTo('GET', path + '/agent-profiles/' + reviews.id)).toHaveLength(1)
  expect(document.body.textContent).toContain('1 existing-agent or other slots')
  act(() => {
    button('Remove Support').click()
  })
  await enter('Offered profiles', 'Triage')
  await chooseProfile('Triage')
  expect(button('Remove Reviews')).toBeDefined()
  await submit()
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Try saving again')
  })
  await submit()
  await waitForUI(() => {
    expect(document.querySelector('[role="dialog"]')).toBeNull()
  })
  const updates = api.requestsTo('PUT', path + '/apps/' + mixedApp.id)
  expect(updates).toHaveLength(2)
  expect(updates[0]?.body).toEqual(updates[1]?.body)
  expect(updates[1]?.body).toEqual({
    name: mixedApp.name,
    enabled: false,
    settings: {
      ...mixedApp.settings,
      launcher: {
        ...mixedApp.settings.launcher,
        slots: [
          { key: 'off-page', agent_profile_id: reviews.id },
          { key: 'profile_1', agent_id: fakeId('agt') },
          { key: 'profile_2', agent_profile_id: triage.id },
        ],
      },
    },
  })
  expect(api.requests.filter((request) => request.method !== 'GET')).toHaveLength(2)
  expect(
    api
      .requestsTo('GET', path + '/agent-profiles')
      .some((request) => request.url.searchParams.get('name') === '*Triage*'),
  ).toBe(true)
})

it.each(['retry', 'remove'] as const)(
  'keeps an unavailable off-page profile selected and supports %s',
  async (action) => {
    let nameAttempts = 0
    const api = fakeApi([
      {
        method: 'GET',
        path: path + '/agent-profiles/' + reviews.id,
        respond: () => {
          nameAttempts++
          return nameAttempts === 1
            ? jsonResponse(
                { code: 'internal_error', error: 'Profile lookup temporarily unavailable' },
                503,
              )
            : Response.json(reviews)
        },
      },
      ...profileNameRoutes,
      {
        method: 'GET',
        path: path + '/agent-profiles',
        respond: () => Response.json({ data: [support], next_cursor: 'page-two' }),
      },
      {
        method: 'GET',
        path: path + '/integration-connections/' + connectionId,
        respond: () => jsonResponse(connection('slack')),
      },
      {
        method: 'PUT',
        path: path + '/apps/' + mixedApp.id,
        respond: ({ body }) =>
          Response.json({ ...mixedApp, ...schemas.zSaveProjectAppRequest.parse(body) }),
      },
    ])
    const closed = vi.fn()
    render(
      api,
      <ProjectAppProfilesDialog
        orgId={orgId}
        projectId={projectId}
        app={mixedApp}
        onOpenChange={closed}
      />,
    )
    await waitForUI(() => {
      expect(document.body.textContent).toContain('Some saved profile names could not be loaded.')
      expect(button(`Remove ${reviews.id}`)).toBeDefined()
    })
    expect(button('Save profiles').disabled).toBe(false)
    if (action === 'retry') {
      act(() => {
        button('Retry profile names').click()
      })
      await waitForUI(() => {
        expect(button('Remove Reviews')).toBeDefined()
        expect(document.body.textContent).not.toContain(
          'Some saved profile names could not be loaded.',
        )
      })
      expect(nameAttempts).toBe(2)
    } else {
      act(() => {
        button(`Remove ${reviews.id}`).click()
      })
      expect(document.body.textContent).not.toContain(
        'Some saved profile names could not be loaded.',
      )
    }
    await submit()
    await waitForUI(() => {
      expect(closed).toHaveBeenCalledWith(false)
    })
    const saved = schemas.zSaveProjectAppRequest.parse(
      api.requestsTo('PUT', path + '/apps/' + mixedApp.id)[0]?.body,
    )
    expect(saved.settings.launcher?.slots).toEqual(
      mixedApp.settings.launcher?.slots.filter(
        (slot) => action === 'retry' || slot.agent_profile_id !== reviews.id,
      ),
    )
    expect(api.requestsTo('GET', path + '/agent-profiles')).toHaveLength(1)
  },
)

it.each([1, 15])(
  'requires a Discord key only for multiple profiles while counting %s fixed agents toward the slot limit',
  async (existingCount) => {
    const selected = connection('discord')
    const app: ProjectApp = {
      ...mixedApp,
      settings: {
        resource: { definition: 'omnara.discord', connection: connectionId, tools: {} },
        launcher: {
          trigger: 'mention',
          scope_kind: 'channel',
          scope_ref: '333',
          slots: [
            { key: 'original', agent_profile_id: support.id },
            ...Array.from({ length: existingCount }, (_, i) => ({
              key: `fixed_${i}`,
              agent_id: fakeId('agt'),
            })),
          ],
        },
      },
    }
    const api = fakeApi([
      ...profileNameRoutes,
      {
        method: 'GET',
        path: path + '/integration-connections/' + connectionId,
        respond: () => jsonResponse(selected),
      },
      {
        method: 'GET',
        path: path + '/agent-profiles',
        respond: () => Response.json({ data: [support, reviews], next_cursor: null }),
      },
      {
        method: 'PUT',
        path: path + '/apps/' + app.id,
        respond: ({ body }) =>
          Response.json({ ...app, ...schemas.zSaveProjectAppRequest.parse(body) }),
      },
    ])
    const closed = vi.fn()
    render(
      api,
      <ProjectAppProfilesDialog
        orgId={orgId}
        projectId={projectId}
        app={app}
        onOpenChange={closed}
      />,
    )
    await waitForUI(() => {
      expect(button('Remove Support')).toBeDefined()
    })
    expect(button('Save profiles').disabled).toBe(false)
    await chooseProfile('Reviews')
    await waitForUI(() => {
      expect(document.body.textContent).toContain(
        existingCount === 15 ? 'at most 16 slots' : 'public_key must be saved',
      )
    })
    expect(button('Save profiles').disabled).toBe(true)
    await submit()
    expect(api.requestsTo('PUT', path + '/apps/' + app.id)).toHaveLength(0)
    act(() => {
      button('Remove Reviews').click()
    })
    expect(button('Save profiles').disabled).toBe(false)
    await submit()
    await waitForUI(() => {
      expect(closed).toHaveBeenCalledWith(false)
    })
    expect(api.requestsTo('PUT', path + '/apps/' + app.id)[0]?.body).toEqual({
      name: app.name,
      enabled: app.enabled,
      settings: app.settings,
    })
  },
)

it.each([false, true])(
  'requires a Discord key for duplicate profile options or an interaction handler (handler=%s)',
  async (handler) => {
    const app: ProjectApp = {
      ...mixedApp,
      settings: {
        resource: { definition: 'omnara.discord', connection: connectionId, tools: {} },
        launcher: {
          trigger: 'mention',
          scope_kind: 'channel',
          scope_ref: '333',
          slots: [
            ...Array.from({ length: handler ? 1 : 2 }, (_, i) => ({
              key: `profile_${i}`,
              agent_profile_id: support.id,
            })),
            { key: 'fixed', agent_id: fakeId('agt') },
          ],
        },
      },
    }
    if (handler)
      app.settings.resource.interaction_handler = { definition: 'omnara.discord.interactions' }
    const api = fakeApi([
      ...profileNameRoutes,
      {
        method: 'GET',
        path: path + '/integration-connections/' + connectionId,
        respond: () => jsonResponse(connection('discord')),
      },
      {
        method: 'GET',
        path: path + '/agent-profiles',
        respond: () => Response.json({ data: [support], next_cursor: null }),
      },
    ])
    const closed = vi.fn()
    render(
      api,
      <ProjectAppProfilesDialog
        orgId={orgId}
        projectId={projectId}
        app={app}
        onOpenChange={closed}
      />,
    )
    await waitForUI(() => {
      expect(document.body.textContent).toContain('public_key must be saved')
    })
    expect(button('Save profiles').disabled).toBe(true)
    await submit()
    expect(closed).not.toHaveBeenCalled()
    expect(api.requestsTo('PUT', path + '/apps/' + app.id)).toHaveLength(0)
  },
)
