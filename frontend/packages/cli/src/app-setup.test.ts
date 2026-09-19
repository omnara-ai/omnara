import { createOmnaraClient, type JsonBody, type SaveProjectAppRequest, schemas } from '@omnara/sdk'
import { expect, it, vi } from 'vitest'
import * as z from 'zod'

import {
  appCommandGroups,
  runAppProfilesUpdate,
  runDiscordAppSetup,
  runGitHubAppSetup,
  zAppProfilesBody,
  zDiscordAppBody,
  zGitHubAppBody,
} from './app-setup.ts'
import { runSlackIntegration, zSlackBody } from './slack-integration.ts'

const id = (prefix: string) => `${prefix}_${'a'.repeat(26)}`
const path = { orgID: id('org'), projectID: id('proj'), agentProfileID: id('aprf') }
const now = '2026-09-18T00:00:00Z'
const report = () => ({
  info: vi.fn(),
  warn: vi.fn(),
  fail: vi.fn(),
  start: vi.fn(),
  stop: vi.fn(),
  done: vi.fn(),
  url: vi.fn(),
})
const json = (value: JsonBody, status = 200) =>
  new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
const connection = (provider: string, state = 'active') => ({
  id: id('iin'),
  org_id: path.orgID,
  project_id: path.projectID,
  provider,
  state,
  provider_tenant_id: '111',
  provider_account_ref: '222',
  provider_agent_display_name: 'Bot',
  provider_config: {},
  created_at: now,
  updated_at: now,
})

it.each([false, true])(
  'Discord multiple profiles require a public key even without agent questions (key=%s)',
  async (hasKey) => {
    const requests: Request[] = []
    const client = createOmnaraClient({
      baseUrl: 'https://omnara.test/api/v1',
      fetch: async (input, init) => {
        const request = new Request(input, init)
        requests.push(request)
        if (request.method === 'GET')
          return json({
            ...connection('discord'),
            provider_config: hasKey ? { public_key: 'ab'.repeat(32) } : {},
          })
        const body = schemas.zSaveProjectAppRequest.parse(await request.clone().json())
        return json(
          z.json().parse({
            ...body,
            id: id('app'),
            project_id: path.projectID,
            created_at: now,
            updated_at: now,
          }),
          201,
        )
      },
    })
    const ids = [path.agentProfileID, `aprf_${'b'.repeat(26)}`]
    const output = report()
    const promise = runDiscordAppSetup({
      client,
      path,
      apiUrl: 'https://omnara.test/api/v1',
      report: output,
      body: zDiscordAppBody.parse({
        name: 'Helpdesk',
        connection: id('iin'),
        channel_id: '333',
        profile_ids: ids,
      }),
    })
    if (hasKey) {
      await promise
      const post = requests.find((request) => request.method === 'POST')
      expect(post).toBeDefined()
      const saved = schemas.zSaveProjectAppRequest.parse(await post?.json())
      expect(saved.settings.launcher?.slots.map((slot) => slot.agent_profile_id)).toEqual(ids)
      expect(saved.settings.resource.interaction_handler).toBeUndefined()
      expect(output.info).toHaveBeenCalledWith(
        `Discord Interactions Endpoint URL: https://omnara.test/api/integrations/discord/${id('iin')}/interactions`,
      )
    } else {
      await expect(promise).rejects.toThrow(/public_key/)
      expect(requests.map((request) => request.method)).toEqual(['GET'])
    }
  },
)

it.each(['slack', 'discord'])(
  'edits saved %s profiles without changing the connection, existing-agent slots, or other settings',
  async (provider) => {
    const second = `aprf_${'b'.repeat(26)}`
    const settings = {
      resource: {
        definition: `omnara.${provider}`,
        connection: id('iin'),
        tools: { [`${provider}_read`]: { deferred: true } },
        listener: { events: ['message'] },
      },
      launcher: {
        trigger: 'mention',
        scope_kind: provider === 'slack' ? 'workspace' : 'channel',
        scope_ref: provider === 'slack' ? 'T123' : '333',
        slots: [
          { key: 'original-key', agent_profile_id: path.agentProfileID },
          { key: 'continue', agent_id: id('agt') },
        ],
      },
    }
    const app = {
      id: id('app'),
      project_id: path.projectID,
      name: 'Helpdesk',
      enabled: false,
      settings,
      created_at: now,
      updated_at: now,
    }
    const requests: Request[] = []
    const client = createOmnaraClient({
      baseUrl: 'https://omnara.test/api/v1',
      fetch: async (input, init) => {
        const request = new Request(input, init)
        requests.push(request)
        if (request.url.includes('integration-connections'))
          return json({ ...connection(provider), provider_config: { public_key: 'ab'.repeat(32) } })
        if (request.method === 'GET') return json(z.json().parse(app))
        const body = schemas.zSaveProjectAppRequest.parse(await request.clone().json())
        return json(z.json().parse({ ...app, ...body }))
      },
    })
    await runAppProfilesUpdate({
      client,
      path: { orgID: path.orgID, projectID: path.projectID, appID: app.id },
      apiUrl: 'https://omnara.test/api/v1',
      report: report(),
      body: zAppProfilesBody.parse({ profile_ids: [path.agentProfileID, second] }),
    })
    expect(requests.filter((request) => request.method !== 'GET')).toHaveLength(1)
    const saved = requests.find((request) => request.method === 'PUT')
    expect(saved).toBeDefined()
    expect(await saved?.json()).toEqual({
      name: app.name,
      enabled: false,
      settings: {
        ...settings,
        launcher: {
          ...settings.launcher,
          slots: [...settings.launcher.slots, { key: 'profile_1', agent_profile_id: second }],
        },
      },
    })
    expect(
      appCommandGroups
        .find((group) => group.name === 'apps')
        ?.operations?.some((operation) => operation.verb === 'profiles'),
    ).toBe(true)
  },
)

it.each([
  { profileSlots: 1, handler: false },
  { profileSlots: 2, handler: false },
  { profileSlots: 1, handler: true },
])(
  'Discord editing counts only profile choices for key validation ($profileSlots profile slots, handler=$handler)',
  async ({ profileSlots, handler }) => {
    const settings: SaveProjectAppRequest['settings'] = {
      resource: { definition: 'omnara.discord', connection: id('iin'), tools: {} },
      launcher: {
        trigger: 'mention',
        scope_kind: 'channel',
        scope_ref: '333',
        slots: [
          ...Array.from({ length: profileSlots }, (_, i) => ({
            key: `profile_${i}`,
            agent_profile_id: path.agentProfileID,
          })),
          { key: 'fixed', agent_id: id('agt') },
        ],
      },
    }
    if (handler)
      settings.resource.interaction_handler = { definition: 'omnara.discord.interactions' }
    const app = {
      id: id('app'),
      project_id: path.projectID,
      name: 'Support',
      enabled: true,
      settings,
      created_at: now,
      updated_at: now,
    }
    const requests: Request[] = []
    const client = createOmnaraClient({
      baseUrl: 'https://omnara.test/api/v1',
      fetch: (input, init) => {
        const request = new Request(input, init)
        requests.push(request)
        if (request.url.includes('integration-connections'))
          return Promise.resolve(json(connection('discord')))
        return Promise.resolve(json(z.json().parse(app)))
      },
    })
    const promise = runAppProfilesUpdate({
      client,
      path: { orgID: path.orgID, projectID: path.projectID, appID: app.id },
      body: zAppProfilesBody.parse({ profile_ids: [path.agentProfileID] }),
      apiUrl: 'https://omnara.test/api/v1',
      report: report(),
    })
    if (profileSlots > 1 || handler) {
      await expect(promise).rejects.toThrow(/public_key/)
      expect(requests.some((request) => request.method !== 'GET')).toBe(false)
    } else {
      await promise
      const saved = requests.find((request) => request.method === 'PUT')
      expect(saved).toBeDefined()
      expect(await saved?.json()).toEqual({ name: app.name, enabled: app.enabled, settings })
    }
  },
)

it.each(['github', 'discord'] as const)(
  'creates %s app settings from an existing project connection',
  async (provider) => {
    const requests: { method: string; body: SaveProjectAppRequest }[] = []
    const client = createOmnaraClient({
      baseUrl: 'https://omnara.test/api/v1',
      fetch: async (input, init) => {
        const request = new Request(input, init)
        if (request.method === 'GET') return json(connection(provider))
        const body = schemas.zSaveProjectAppRequest.parse(await request.json())
        requests.push({ method: request.method, body })
        return json(
          z.json().parse({
            ...body,
            id: id('app'),
            project_id: path.projectID,
            created_at: now,
            updated_at: now,
          }),
          201,
        )
      },
    })
    const output = report()
    const context = { client, path, apiUrl: 'https://omnara.test/api/v1', report: output }
    if (provider === 'github')
      await runGitHubAppSetup({
        ...context,
        body: zGitHubAppBody.parse({
          name: 'Reviewer',
          connection: id('iin'),
          repository_id: '9007199254740993',
          tools: ['github_read'],
        }),
      })
    else
      await runDiscordAppSetup({
        ...context,
        body: zDiscordAppBody.parse({
          name: 'Support',
          connection: id('iin'),
          channel_id: '333',
          listen: false,
          tools: [],
        }),
      })
    expect(requests).toHaveLength(1)
    expect(requests[0]?.body).toMatchObject({
      settings: {
        resource: {
          definition: `omnara.${provider}`,
          connection: id('iin'),
          tools: provider === 'github' ? { github_read: {} } : {},
        },
        launcher: {
          scope_kind: provider === 'github' ? 'repository' : 'channel',
          scope_ref: provider === 'github' ? '9007199254740993' : '333',
          slots: [{ key: 'default', agent_profile_id: path.agentProfileID }],
        },
      },
    })
    expect(output.info).toHaveBeenCalledWith(`App ID: ${id('app')}`)
    expect(output.done).toHaveBeenCalledOnce()
  },
)

it.each([
  ['slack', 'active'],
  ['github', 'disabled'],
])('rejects %s/%s connection before creating an app', async (provider, state) => {
  const fetcher = vi.fn(() => Promise.resolve(json(connection(provider, state))))
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: fetcher })
  await expect(
    runGitHubAppSetup({
      client,
      path,
      apiUrl: 'https://omnara.test/api/v1',
      report: report(),
      body: zGitHubAppBody.parse({ name: 'Review', connection: id('iin'), repository_id: '123' }),
    }),
  ).rejects.toThrow('active github connection')
  expect(fetcher).toHaveBeenCalledOnce()
})

it('Slack reports preserved app setup across pages without assuming a connection profile', async () => {
  const requests: URL[] = []
  const client = createOmnaraClient({
    baseUrl: 'https://omnara.test/api/v1',
    fetch: async (input, init) => {
      const request = new Request(input, init),
        url = new URL(request.url)
      await request.text()
      requests.push(url)
      if (url.pathname.endsWith('/integration-oauth/setup'))
        return json(
          {
            provider: 'slack',
            flow_id: id('ioaf'),
            oauth_url: 'https://slack.test/authorize',
            redirect_uri: 'https://omnara.test/api/integrations/oauth/callback',
            events_url: 'https://omnara.test/events',
            actions_url: 'https://omnara.test/actions',
            expires_at: new Date(Date.now() + 60_000).toISOString(),
          },
          201,
        )
      if (url.pathname.endsWith('/integration-connections'))
        return json({ data: [connection('slack')], next_cursor: null })
      if (!url.searchParams.has('cursor')) return json({ data: [], next_cursor: 'next-app-page' })
      return json({
        data: [
          {
            id: id('app'),
            project_id: path.projectID,
            name: 'Preserved setup',
            enabled: false,
            settings: { resource: { definition: 'omnara.slack', connection: id('iin') } },
            created_at: now,
            updated_at: now,
          },
        ],
        next_cursor: null,
      })
    },
  })
  const output = report()
  await runSlackIntegration({
    client,
    path,
    apiUrl: 'https://omnara.test/api/v1',
    report: output,
    body: zSlackBody.parse({
      client_id: 'client',
      client_secret: 'secret',
      signing_secret: 'signing',
      browser: false,
    }),
  })
  expect(
    requests
      .find((r) => r.pathname.endsWith('/integration-connections'))
      ?.searchParams.get('oauth_flow_id'),
  ).toBe(id('ioaf'))
  expect(requests.at(-1)?.searchParams.get('cursor')).toBe('next-app-page')
  expect(output.info).toHaveBeenCalledWith(`App ID: ${id('app')} (Preserved setup; disabled)`)
})
