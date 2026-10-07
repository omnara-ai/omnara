/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import {
  type AgentProfileSummary,
  createOmnaraClient,
  type CronTrigger,
  type Integration,
  schemas,
} from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { IntegrationDetail } from '@/routes/IntegrationPage'
import { type FakeApi, fakeApi, jsonResponse } from '@/test/fake-api'
import { agentConfigModel, fakeId, integration as integrationFixture } from '@/test/fixtures'
import { renderIntegration } from '@/test/integration-render'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, choose, enter, field, waitForUI } from '@/test/secret-editor'

import { IntegrationForm } from './IntegrationForm'

const orgId = fakeId('org'),
  projectId = fakeId('proj')
const path = `/api/v1/orgs/${orgId}/projects/${projectId}`
const now = '2026-09-18T00:00:00Z'
function profile(letter: string, name: string): AgentProfileSummary {
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
function profileDetail(item: AgentProfileSummary) {
  return {
    ...item,
    current_config: {
      ...item.current_config,
      compiled_definition: {
        instruction: 'Help with this integration.',
        model: { configured_model_id: fakeId('mdl') },
      },
    },
  }
}
const profileNameRoutes = [support, reviews, triage].map((item) => ({
  method: 'GET',
  path: path + '/agent-profiles/' + item.id,
  respond: () => Response.json(profileDetail(item)),
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
  vi.restoreAllMocks()
})
function render(api: FakeApi, node: ReactNode) {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  const rerender = (next: ReactNode) => {
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>{next}</QueryClientProvider>
        </OmnaraClientProvider>,
      )
    })
  }
  rerender(node)
  return { rerender }
}
function profilePicker(scope: ParentNode | null = container) {
  const label = [...(scope?.querySelectorAll('label') ?? [])].find(
    (item) => item.textContent === 'Agent profile',
  )
  const picker = label && document.getElementById(label.htmlFor)
  if (!(picker instanceof HTMLButtonElement)) throw new Error('Missing agent profile picker')
  return picker
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
    if (!form) throw new Error('Missing integration form')
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
const mixedIntegration: Integration = {
  ...integrationFixture({ name: 'shared-support', state: 'active', provider_tenant_id: 'T123' }),
  settings: {
    launcher: {
      profiles: [support.id, reviews.id],
    },
  },
}

it('saves independent GitHub triggers and clears the profile only after saving both off', async () => {
  const integration = integrationFixture({ integration_kind: 'github_pr' })
  const updatePath = path + '/integrations/' + integration.id
  let revision = 0
  const onSaved = vi.fn()
  const api = fakeApi([
    ...profileNameRoutes,
    {
      method: 'GET',
      path: path + '/agent-profiles',
      respond: () => Response.json({ data: [reviews], next_cursor: null }),
    },
    {
      method: 'PUT',
      path: updatePath,
      respond: ({ body }) =>
        Response.json({
          ...integration,
          ...schemas.zUpdateIntegrationRequest.parse(body),
          updated_at: `2026-09-20T00:00:0${++revision}Z`,
        }),
    },
  ])
  render(
    api,
    <IntegrationForm
      orgId={orgId}
      projectId={projectId}
      integrationKind="github_pr"
      integration={integration}
      defaultLauncherEnabled
      onSaved={onSaved}
    />,
  )
  expect(field('PR opened')).toHaveProperty('checked', true)
  expect(field('Bot mentioned')).toHaveProperty('checked', true)
  expect(button('Save changes').disabled).toBe(true)
  await submit()
  expect(api.requestsTo('PUT', updatePath)).toHaveLength(0)
  await choose('Agent profile', 'Reviews')
  for (const [toggle, trigger] of [
    [null, 'both'],
    ['PR opened', 'mention'],
    ['Bot mentioned', null],
    ['PR opened', 'pull_request_opened'],
    ['Bot mentioned', 'both'],
  ] as const) {
    if (toggle)
      act(() => {
        field(toggle).click()
      })
    if (trigger === 'pull_request_opened') {
      expect(button('Save changes').disabled).toBe(true)
      expect(profilePicker().textContent).not.toContain('Reviews')
      await submit()
      expect(api.requestsTo('PUT', updatePath)).toHaveLength(3)
      await choose('Agent profile', 'Reviews')
    }
    if (trigger === null) {
      expect(profilePicker().textContent).toContain('Reviews')
      expect(container.textContent).toContain(
        'Saving with both triggers off clears the selected profile.',
      )
    }
    expect(button('Save changes').disabled).toBe(false)
    await submit()
    await waitForUI(() => {
      expect(onSaved).toHaveBeenCalledTimes(revision)
      expect(button('Save changes').disabled).toBe(true)
      expect(() => button('Discard changes')).toThrow('Missing button')
    })
    expect(api.requestsTo('PUT', updatePath).at(-1)?.body).toEqual({
      settings: trigger ? { launcher: { profile: reviews.id, trigger } } : {},
    })
  }
})

it('keeps off-page profiles and retries a failed profile save', async () => {
  let attempts = 0
  const api = fakeApi([
    ...profileNameRoutes,
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
      path: path + '/integrations/' + mixedIntegration.id,
      respond: ({ body }) => {
        attempts++
        if (attempts === 1)
          return jsonResponse({ code: 'conflict', error: 'Try saving again' }, 409)
        return Response.json({
          ...mixedIntegration,
          ...schemas.zUpdateIntegrationRequest.parse(body),
          updated_at: '2026-09-21T00:00:00Z',
        })
      },
    },
  ])
  const closed = vi.fn()
  render(
    api,
    <IntegrationForm
      orgId={orgId}
      projectId={projectId}
      integration={mixedIntegration}
      integrationKind="slack_thread"
      onSaved={closed}
    />,
  )
  await waitForUI(() => {
    expect(button('Remove Support')).toBeDefined()
    expect(button('Remove Reviews')).toBeDefined()
  })
  expect(api.requestsTo('GET', path + '/agent-profiles/' + reviews.id)).toHaveLength(1)
  act(() => {
    button('Remove Support').click()
  })
  await enter('Profiles for mentions', 'Triage')
  await chooseProfile('Triage')
  expect(button('Remove Reviews')).toBeDefined()
  await submit()
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Try saving again')
  })
  await submit()
  await waitForUI(() => {
    expect(closed).toHaveBeenCalledWith(expect.objectContaining({ id: fakeId('itg') }))
  })
  const updates = api.requestsTo('PUT', path + '/integrations/' + mixedIntegration.id)
  expect(updates).toHaveLength(2)
  expect(updates[0]?.body).toEqual(updates[1]?.body)
  expect(updates[1]?.body).toEqual({
    settings: { launcher: { profiles: [reviews.id, triage.id] } },
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
            : Response.json(profileDetail(reviews))
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
        path: path + '/integrations/' + mixedIntegration.id,
        respond: ({ body }) =>
          Response.json({
            ...mixedIntegration,
            ...schemas.zUpdateIntegrationRequest.parse(body),
            updated_at: '2026-09-21T00:00:00Z',
          }),
      },
    ])
    const closed = vi.fn()
    render(
      api,
      <IntegrationForm
        orgId={orgId}
        projectId={projectId}
        integration={mixedIntegration}
        integrationKind="slack_thread"
        onSaved={closed}
      />,
    )
    await waitForUI(() => {
      expect(document.body.textContent).toContain('Some saved profile names could not be loaded.')
      expect(button(`Remove ${reviews.id}`)).toBeDefined()
    })
    expect(button('Save changes').disabled).toBe(true)
    await submit()
    expect(api.requestsTo('PUT', path + '/integrations/' + mixedIntegration.id)).toHaveLength(0)
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
      act(() => {
        button('Remove Support').click()
      })
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
      expect(closed).toHaveBeenCalledWith(expect.objectContaining({ id: fakeId('itg') }))
    })
    const saved = schemas.zUpdateIntegrationRequest.parse(
      api.requestsTo('PUT', path + '/integrations/' + mixedIntegration.id)[0]?.body,
    )
    expect(saved.settings.launcher).toEqual({
      profiles: action === 'retry' ? [reviews.id] : [support.id],
    })
    expect(api.requestsTo('GET', path + '/agent-profiles')).toHaveLength(1)
  },
)

it('requires a public key for even one Discord launch profile', async () => {
  const integration = {
    ...mixedIntegration,
    integration_kind: 'discord_thread' as const,
    settings: {},
  }
  const api = fakeApi(launcherRoutes)
  render(
    api,
    <IntegrationForm
      orgId={orgId}
      projectId={projectId}
      integration={integration}
      integrationKind="discord_thread"
      onSaved={vi.fn()}
    />,
  )
  await chooseProfile('Support')
  expect(button('Discard changes')).toBeDefined()
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Configure a valid Discord public key')
  })
  expect(button('Save changes').disabled).toBe(true)
  await submit()
  expect(api.requests.filter((request) => request.method !== 'GET')).toHaveLength(0)
})

const launcherRoutes = [
  ...profileNameRoutes,
  {
    method: 'GET',
    path: path + '/agent-profiles',
    respond: () => Response.json({ data: [support, reviews, triage], next_cursor: null }),
  },
]

it('refreshes a clean inline form from a newer remote record without making it dirty', async () => {
  const integration = integrationFixture({
    integration_kind: 'github_pr',
    settings: { launcher: { profile: support.id, trigger: 'both', repository_id: '123' } },
  })
  const api = fakeApi(launcherRoutes)
  const props = { orgId, projectId, integrationKind: 'github_pr' as const, onSaved: vi.fn() }
  const { rerender } = render(api, <IntegrationForm {...props} integration={integration} />)
  await waitForUI(() => {
    expect(profilePicker().textContent).toContain('Support')
  })
  const form = container.querySelector('form')
  expect(form).not.toBeNull()
  expect(button('Save changes').disabled).toBe(true)
  for (const label of ['Edit', 'Choose a profile', 'Choose profiles', 'Discard changes'])
    expect(() => button(label)).toThrow('Missing button')
  rerender(<IntegrationForm {...props} integration={{ ...integration }} />)
  expect(button('Save changes').disabled).toBe(true)
  const next = {
    ...integration,
    updated_at: '2026-09-20T00:00:00Z',
    settings: { launcher: { profile: reviews.id, trigger: 'mention', repository_id: '456' } },
  }
  rerender(<IntegrationForm {...props} integration={next} />)
  await waitForUI(() => {
    expect(profilePicker().textContent).toContain('Reviews')
  })
  expect(container.querySelector('form')).toBe(form)
  expect(field('PR opened')).toHaveProperty('checked', false)
  expect(field('Bot mentioned')).toHaveProperty('checked', true)
  expect(container.textContent).toContain('Restricted to repository 456.')
  expect(container.textContent).not.toContain('Integration changed.')
  expect(button('Save changes').disabled).toBe(true)
  expect(() => button('Discard changes')).toThrow('Missing button')
  await submit()
  expect(api.requests.filter((request) => request.method !== 'GET')).toHaveLength(0)
})

it.each(['valid', 'incomplete'] as const)(
  'retains a %s draft across prop identities, blocks stale saves and reloads the latest settings',
  async (draft) => {
    const integration = integrationFixture({
      integration_kind: 'github_pr',
      bot_mention: '@reviewer',
      settings:
        draft === 'valid'
          ? { launcher: { profile: support.id, trigger: 'both', repository_id: '123' } }
          : {},
    })
    const api = fakeApi(launcherRoutes)
    const onDiscard = vi.fn()
    const props = {
      orgId,
      projectId,
      integrationKind: 'github_pr' as const,
      onSaved: vi.fn(),
      onDiscard,
    }
    const { rerender } = render(api, <IntegrationForm {...props} integration={integration} />)
    act(() => {
      field('PR opened').click()
    })
    const form = container.querySelector('form')
    const opened = draft === 'incomplete'
    expect(button('Save changes').disabled).toBe(opened)
    expect(button('Discard changes').disabled).toBe(false)
    rerender(
      <IntegrationForm
        {...props}
        integration={{
          ...integration,
          runtime_failure: { message: 'Connection temporarily unavailable', retry_at: now },
        }}
      />,
    )
    expect(container.querySelector('form')).toBe(form)
    expect(field('PR opened')).toHaveProperty('checked', opened)
    expect(field('Bot mentioned')).toHaveProperty('checked', !opened)
    expect(container.textContent).not.toContain('Integration changed.')
    expect(button('Save changes').disabled).toBe(opened)
    const next = {
      ...integration,
      updated_at: '2026-09-20T00:00:00Z',
      settings: { launcher: { profile: reviews.id, trigger: 'both', repository_id: '456' } },
    }
    rerender(<IntegrationForm {...props} integration={next} />)
    expect(container.textContent).toContain('Integration changed. Reload settings')
    expect(container.querySelector('form')).toBe(form)
    expect(field('PR opened')).toHaveProperty('checked', opened)
    expect(field('Bot mentioned')).toHaveProperty('checked', !opened)
    expect(field('PR opened').disabled).toBe(true)
    expect(profilePicker().disabled).toBe(true)
    expect(button('Save changes').disabled).toBe(true)
    expect(() => button('Discard changes')).toThrow('Missing button')
    if (draft === 'valid') {
      const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
      await act(async () => {
        button('Copy bot mention').click()
        await Promise.resolve()
      })
      expect(copy).toHaveBeenCalledWith('@reviewer please review this PR')
    } else expect(() => button('Copy bot mention')).toThrow('Missing button')
    rerender(<IntegrationForm {...props} integration={{ ...next }} />)
    expect(container.textContent).toContain('Integration changed. Reload settings')
    await submit()
    expect(api.requests.filter((request) => request.method !== 'GET')).toHaveLength(0)
    expect(props.onSaved).not.toHaveBeenCalled()
    act(() => {
      button('Reload settings').click()
    })
    await waitForUI(() => {
      expect(profilePicker().textContent).toContain('Reviews')
    })
    expect(onDiscard).toHaveBeenCalledOnce()
    expect(container.textContent).not.toContain('Integration changed.')
    expect(container.textContent).toContain('Restricted to repository 456.')
    expect(field('PR opened')).toHaveProperty('checked', true)
    expect(field('Bot mentioned')).toHaveProperty('checked', true)
    expect(field('PR opened').disabled).toBe(false)
    expect(button('Save changes').disabled).toBe(true)
    expect(() => button('Discard changes')).toThrow('Missing button')
  },
)

it.each(['immediate', 'delayed'] as const)(
  'rebases on the saved response and stays clean with a %s parent update',
  async (parentUpdate) => {
    const integration = integrationFixture({
      integration_kind: 'github_pr',
      settings: { launcher: { profile: support.id, trigger: 'both' } },
    })
    const updatePath = path + '/integrations/' + integration.id
    let saved = integration
    const api = fakeApi([
      ...launcherRoutes,
      {
        method: 'PUT',
        path: updatePath,
        respond: ({ body }) => {
          saved = {
            ...integration,
            ...schemas.zUpdateIntegrationRequest.parse(body),
            updated_at: '2026-09-20T00:00:00Z',
          }
          return Response.json(saved)
        },
      },
    ])
    const props = {
      orgId,
      projectId,
      integrationKind: 'github_pr' as const,
      onSaved: vi.fn((result: Integration) => {
        if (parentUpdate === 'immediate')
          rerender(<IntegrationForm {...props} integration={result} />)
      }),
    }
    const { rerender } = render(api, <IntegrationForm {...props} integration={integration} />)
    const form = container.querySelector('form')
    act(() => {
      field('PR opened').click()
    })
    await submit()
    await waitForUI(() => {
      expect(props.onSaved).toHaveBeenCalledWith(saved)
      expect(button('Save changes').disabled).toBe(true)
    })
    expect(container.querySelector('form')).toBe(form)
    expect(field('PR opened')).toHaveProperty('checked', false)
    expect(field('Bot mentioned')).toHaveProperty('checked', true)
    expect(() => button('Discard changes')).toThrow('Missing button')
    expect(container.textContent).not.toContain('Integration changed.')
    await submit()
    expect(api.requestsTo('PUT', updatePath)).toHaveLength(1)
    if (parentUpdate === 'delayed') {
      rerender(<IntegrationForm {...props} integration={{ ...integration }} />)
      expect(button('Save changes').disabled).toBe(true)
      expect(field('PR opened')).toHaveProperty('checked', false)
      act(() => {
        field('PR opened').click()
      })
      act(() => {
        button('Discard changes').click()
      })
      expect(field('PR opened')).toHaveProperty('checked', false)
      expect(button('Save changes').disabled).toBe(true)
    }
    act(() => {
      field('PR opened').click()
    })
    expect(button('Save changes').disabled).toBe(false)
    rerender(<IntegrationForm {...props} integration={{ ...saved }} />)
    expect(container.textContent).not.toContain('Integration changed.')
    expect(field('PR opened')).toHaveProperty('checked', true)
    expect(button('Save changes').disabled).toBe(false)
    act(() => {
      button('Discard changes').click()
    })
    expect(field('PR opened')).toHaveProperty('checked', false)
    expect(field('Bot mentioned')).toHaveProperty('checked', true)
    expect(button('Save changes').disabled).toBe(true)
    expect(api.requestsTo('PUT', updatePath)[0]?.body).toEqual({
      settings: { launcher: { profile: support.id, trigger: 'mention' } },
    })
  },
)

it.each(['github_pr', 'slack_thread', 'discord_thread'] as const)(
  'disables %s controls and rejects programmatic submission after edit access is removed',
  async (integrationKind) => {
    const integration = integrationFixture({
      integration_kind: integrationKind,
      bot_mention: integrationKind === 'github_pr' ? '@reviewer' : undefined,
      provider_config: { public_key: 'ab'.repeat(32) },
      settings: {
        launcher:
          integrationKind === 'github_pr'
            ? { profile: support.id, trigger: 'both' }
            : { profiles: [support.id, reviews.id] },
      },
    })
    const api = fakeApi(launcherRoutes)
    const props = { orgId, projectId, integrationKind, integration, onSaved: vi.fn() }
    const { rerender } = render(api, <IntegrationForm {...props} />)
    if (integrationKind === 'github_pr') {
      act(() => {
        field('PR opened').click()
      })
    } else {
      await waitForUI(() => {
        expect(button('Remove Support')).toBeDefined()
      })
      act(() => {
        button('Remove Support').click()
      })
    }
    expect(button('Save changes').disabled).toBe(false)
    rerender(<IntegrationForm {...props} canEdit={false} />)
    expect(container.querySelector('form')).not.toBeNull()
    expect(() => button('Save changes')).toThrow('Missing button')
    expect(() => button('Discard changes')).toThrow('Missing button')
    if (integrationKind === 'github_pr') {
      expect(field('PR opened').disabled).toBe(true)
      expect(field('Bot mentioned').disabled).toBe(true)
      expect(profilePicker().disabled).toBe(true)
      const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
      await act(async () => {
        button('Copy bot mention').click()
        await Promise.resolve()
      })
      expect(copy).toHaveBeenCalledWith('@reviewer please review this PR')
    } else {
      expect(field('Profiles for mentions').disabled).toBe(true)
      await waitForUI(() => {
        expect(button('Remove Reviews').getAttribute('aria-disabled')).toBe('true')
      })
      act(() => {
        button('Remove Reviews').click()
      })
      expect(button('Remove Reviews')).toBeDefined()
    }
    await submit()
    expect(props.onSaved).not.toHaveBeenCalled()
    expect(api.requests.filter((request) => request.method !== 'GET')).toHaveLength(0)
  },
)

it.each(['slack_thread', 'discord_thread'] as const)(
  'keeps %s schedules and their channels independent when mention profiles are changed or cleared',
  async (integrationKind) => {
    let integration = integrationFixture({
      integration_kind: integrationKind,
      state: 'active',
      provider_tenant_id: integrationKind === 'slack_thread' ? 'T123' : '111',
      provider_config: { public_key: 'ab'.repeat(32) },
      settings: { launcher: { profiles: [support.id, reviews.id] } },
    })
    const channel = integrationKind === 'slack_thread' ? 'C123' : '123456'
    const schedule: CronTrigger = {
      id: fakeId('cron'),
      org_id: orgId,
      project_id: projectId,
      name: 'daily-report',
      cron: '0 9 * * 1-5',
      timezone: 'UTC',
      enabled: true,
      target: {
        type: 'integration',
        integration_id: integration.id,
        settings: {
          agent_profile_id: triage.id,
          channel_id: channel,
          opening_message_template: '{{.trigger.name}} — {{.trigger.local_date}}',
          message_template: 'Summarize the daily progress.',
        },
      },
      last_fired_at: null,
      next_fire_at: now,
      failure_report: null,
      last_run: null,
      created_at: now,
      updated_at: now,
    }
    const detailPath = path + '/integrations/' + integration.id
    let revision = 0
    const api = fakeApi([
      ...launcherRoutes,
      { method: 'GET', path: detailPath, respond: () => Response.json(integration) },
      {
        method: 'GET',
        path: detailPath + '/subscriptions',
        respond: () => Response.json({ data: [], next_cursor: null }),
      },
      {
        method: 'GET',
        path: path + '/cron-triggers',
        respond: () => Response.json({ data: [schedule], next_cursor: null }),
      },
      {
        method: 'PUT',
        path: detailPath,
        respond: ({ body }) => {
          integration = {
            ...integration,
            ...schemas.zUpdateIntegrationRequest.parse(body),
            updated_at: `2026-09-20T00:00:0${++revision}Z`,
          }
          return Response.json(integration)
        },
      },
    ])
    const rendered = renderIntegration(
      root,
      api,
      <IntegrationDetail
        orgId={orgId}
        projectId={projectId}
        integrationId={integration.id}
        canManage
      />,
    )
    cache = rendered.cache
    await waitForUI(() => {
      expect(button('Remove Support')).toBeDefined()
      expect(button('Remove Reviews')).toBeDefined()
      expect(button('Edit schedule daily-report')).toBeDefined()
    })
    const mentions = container.querySelector('[aria-label="Mentions"]')
    expect(mentions?.textContent).not.toContain('Triage')
    expect(button('Save changes').disabled).toBe(true)
    for (const [index, name] of ['Support', 'Reviews'].entries()) {
      act(() => {
        button(`Remove ${name}`).click()
      })
      act(() => {
        button('Save changes').click()
      })
      await waitForUI(() => {
        expect(api.requestsTo('PUT', detailPath)).toHaveLength(index + 1)
        expect(button('Save changes').disabled).toBe(true)
      })
      expect(api.requestsTo('PUT', detailPath)[index]?.body).toEqual({
        settings: index === 0 ? { launcher: { profiles: [reviews.id] } } : {},
      })
      expect(button('Edit schedule daily-report')).toBeDefined()
      expect(button('Add schedule').disabled).toBe(false)
    }
    expect(mentions?.textContent).toContain('Choose a profile to enable mentions.')
    act(() => {
      button('Edit schedule daily-report').click()
    })
    await waitForUI(() => {
      expect(field('Channel ID').value).toBe(channel)
      expect(profilePicker(document.querySelector('[role="dialog"]')).textContent).toContain(
        'Triage',
      )
    })
    expect(field('Task instructions').value).toBe('Summarize the daily progress.')
    expect(
      api.requests
        .filter((request) => request.method !== 'GET')
        .map((request) => request.url.pathname),
    ).toEqual([detailPath, detailPath])
    act(() => {
      button('Close').click()
    })
    await waitForUI(() => {
      expect(document.querySelector('[role="dialog"]')).toBeNull()
    })
  },
)
