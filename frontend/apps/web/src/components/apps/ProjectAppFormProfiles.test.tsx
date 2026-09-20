/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { type AgentProfile, createOmnaraClient, type ProjectApp, schemas } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { type FakeApi, fakeApi, jsonResponse } from '@/test/fake-api'
import { agentConfigModel, fakeId, projectApp } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, waitForUI } from '@/test/secret-editor'

import { ProjectAppForm } from './ProjectAppForm'

const orgId = fakeId('org'),
  projectId = fakeId('proj')
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
const mixedApp: ProjectApp = {
  ...projectApp({ name: 'shared-support', state: 'active', provider_tenant_id: 'T123' }),
  settings: {
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

it('keeps off-page and existing-agent slots and retries a failed profile save', async () => {
  let attempts = 0
  const api = fakeApi([
    ...profileNameRoutes,
    {
      method: 'GET',
      path: path + '/apps',
      respond: () => Response.json({ data: [mixedApp], next_cursor: null }),
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
  const closed = vi.fn()
  render(
    api,
    <ProjectAppForm
      orgId={orgId}
      projectId={projectId}
      app={mixedApp}
      provider="slack"
      onSaved={closed}
    />,
  )
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
    expect(closed).toHaveBeenCalledWith(expect.objectContaining({ id: fakeId('app') }))
  })
  const updates = api.requestsTo('PUT', path + '/apps/' + mixedApp.id)
  expect(updates).toHaveLength(2)
  expect(updates[0]?.body).toEqual(updates[1]?.body)
  expect(updates[1]?.body).toEqual({
    name: mixedApp.name,
    definition_id: mixedApp.definition_id,
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
        method: 'PUT',
        path: path + '/apps/' + mixedApp.id,
        respond: ({ body }) =>
          Response.json({ ...mixedApp, ...schemas.zSaveProjectAppRequest.parse(body) }),
      },
    ])
    const closed = vi.fn()
    render(
      api,
      <ProjectAppForm
        orgId={orgId}
        projectId={projectId}
        app={mixedApp}
        provider="slack"
        onSaved={closed}
      />,
    )
    await waitForUI(() => {
      expect(document.body.textContent).toContain('Some saved profile names could not be loaded.')
      expect(button(`Remove ${reviews.id}`)).toBeDefined()
    })
    expect(button('Save changes').disabled).toBe(false)
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
      expect(closed).toHaveBeenCalledWith(expect.objectContaining({ id: fakeId('app') }))
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
  'keeps the Discord public key while counting %s fixed agents toward the slot limit',
  async (existingCount) => {
    const app: ProjectApp = {
      ...mixedApp,
      definition_id: 'omnara.discord',
      provider: 'discord',
      provider_config: { public_key: 'ab'.repeat(32) },
      settings: {
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
      <ProjectAppForm
        orgId={orgId}
        projectId={projectId}
        app={app}
        provider="discord"
        onSaved={closed}
      />,
    )
    await waitForUI(() => {
      expect(button('Remove Support')).toBeDefined()
    })
    expect(button('Save changes').disabled).toBe(false)
    await chooseProfile('Reviews')
    if (existingCount === 15) {
      await waitForUI(() => {
        expect(document.body.textContent).toContain('at most 16 slots')
      })
      expect(button('Save changes').disabled).toBe(true)
      await submit()
      expect(api.requestsTo('PUT', path + '/apps/' + app.id)).toHaveLength(0)
    } else {
      expect(button('Save changes').disabled).toBe(false)
    }
    act(() => {
      button('Remove Reviews').click()
    })
    expect(button('Save changes').disabled).toBe(false)
    await submit()
    await waitForUI(() => {
      expect(closed).toHaveBeenCalledWith(expect.objectContaining({ id: fakeId('app') }))
    })
    expect(api.requestsTo('PUT', path + '/apps/' + app.id)[0]?.body).toEqual({
      name: app.name,
      definition_id: app.definition_id,
      settings: app.settings,
    })
  },
)

it('requires a public key for even one Discord launch profile', async () => {
  const app = {
    ...mixedApp,
    definition_id: 'omnara.discord',
    provider: 'discord' as const,
    settings: {
      launcher: {
        trigger: 'mention',
        scope_kind: 'channel',
        scope_ref: '333',
        slots: [{ key: 'default', agent_profile_id: support.id }],
      },
    },
  }
  const api = fakeApi([
    ...profileNameRoutes,
    {
      method: 'GET',
      path: path + '/agent-profiles',
      respond: () => Response.json({ data: [support], next_cursor: null }),
    },
  ])
  render(
    api,
    <ProjectAppForm
      orgId={orgId}
      projectId={projectId}
      app={app}
      provider="discord"
      onSaved={vi.fn()}
    />,
  )
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Configure a valid Discord public key')
  })
  expect(button('Save changes').disabled).toBe(true)
  await submit()
  expect(api.requests.filter((request) => request.method !== 'GET')).toHaveLength(0)
})

it.each(['saved channel', 'new launcher'] as const)(
  'uses the verified Slack workspace when switching from %s',
  async (scenario) => {
    const app = {
      ...mixedApp,
      settings: scenario === 'saved channel' ? mixedApp.settings : {},
    }
    const api = fakeApi([
      ...profileNameRoutes,
      {
        method: 'GET',
        path: path + '/agent-profiles',
        respond: () => Response.json({ data: [support], next_cursor: null }),
      },
      {
        method: 'PUT',
        path: path + '/apps/' + app.id,
        respond: ({ body }) =>
          Response.json({ ...app, ...schemas.zSaveProjectAppRequest.parse(body) }),
      },
    ])
    const onSaved = vi.fn()
    render(
      api,
      <ProjectAppForm
        orgId={orgId}
        projectId={projectId}
        provider="slack"
        app={app}
        onSaved={onSaved}
      />,
    )
    function selectScope(value: string) {
      act(() => {
        const select = container.querySelector<HTMLSelectElement>('select[aria-label="Launch in"]')
        if (!select) throw new Error('Missing launcher scope selector')
        select.value = value
        select.dispatchEvent(new Event('change', { bubbles: true }))
      })
    }
    if (scenario === 'new launcher') {
      act(() => {
        container.querySelector<HTMLInputElement>('input[name="launcher"]')?.click()
      })
      await chooseProfile('Support')
      expect(container.textContent).toContain('Workspace: T123')
      selectScope('channel')
      await enter('Channel ID', 'C456')
    }
    selectScope('workspace')
    expect(container.textContent).toContain('Workspace: T123')
    expect(container.textContent).not.toContain('Enter a Slack workspace ID')
    expect(button('Save changes').disabled).toBe(false)
    await submit()
    await waitForUI(() => {
      expect(onSaved).toHaveBeenCalled()
    })
    const updates = api.requestsTo('PUT', path + '/apps/' + app.id)
    expect(updates).toHaveLength(1)
    expect(updates[0]?.body).toMatchObject({
      settings: {
        launcher: {
          trigger: 'mention',
          scope_kind: 'workspace',
          scope_ref: 'T123',
          slots:
            scenario === 'saved channel'
              ? mixedApp.settings.launcher?.slots
              : [{ key: 'default', agent_profile_id: support.id }],
        },
      },
    })
  },
)
