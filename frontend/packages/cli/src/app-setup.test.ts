import { type AppType, createOmnaraClient, type JsonBody, schemas } from '@omnara/sdk'
import { expect, it, vi } from 'vitest'
import * as z from 'zod'

import { runAppProfilesUpdate, zAppProfilesBody } from './app-setup.ts'
import { runSlackIntegration, zSlackBody } from './slack-integration.ts'

const id = (prefix: string) => `${prefix}_${'a'.repeat(26)}`
const path = { orgID: id('org'), projectID: id('proj'), appID: id('app') }
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
const app = (appType: AppType) => ({
  id: path.appID,
  project_id: path.projectID,
  name: 'support',
  app_type: appType,
  state: 'active',
  setup_revision: 2,
  settings: {},
  provider_tenant_id: '111',
  provider_account_ref: '222',
  provider_agent_display_name: 'Bot',
  provider_config: {},
  capabilities: { tools: {}, subscriptions: {} },
  created_at: now,
  updated_at: now,
})

it.each(['slack_thread', 'discord_thread'] as const)(
  'edits %s profile choices without touching credentials or existing-agent slots',
  async (appType) => {
    const second = `aprf_${'b'.repeat(26)}`
    const saved = {
      ...app(appType),
      settings: {
        launcher: {
          trigger: 'mention',
          scope_kind: appType === 'slack_thread' ? 'workspace' : 'channel',
          scope_ref: appType === 'slack_thread' ? 'T123' : '333',
          slots: [
            { key: 'original', agent_profile_id: id('aprf') },
            { key: 'continue', agent_id: id('agt') },
          ],
        },
      },
    }
    const requests: Request[] = []
    const client = createOmnaraClient({
      baseUrl: 'https://omnara.test/api/v1',
      fetch: async (input, init) => {
        const request = new Request(input, init)
        requests.push(request)
        if (request.method === 'GET') return json(z.json().parse(saved))
        const body = schemas.zSaveProjectAppRequest.parse(await request.clone().json())
        expect(body).toMatchObject({ name: saved.name, app_type: saved.app_type })
        expect(body.settings.launcher?.slots).toEqual([
          { key: 'original', agent_profile_id: id('aprf') },
          { key: 'continue', agent_id: id('agt') },
          { key: 'profile_1', agent_profile_id: second },
        ])
        return json(z.json().parse({ ...saved, ...body }))
      },
    })
    await runAppProfilesUpdate({
      client,
      path,
      apiUrl: 'https://omnara.test/api/v1',
      report: report(),
      body: zAppProfilesBody.parse({ profile_ids: [id('aprf'), second] }),
    })
    expect(requests.map((request) => request.method)).toEqual(['GET', 'PUT'])
    expect(
      requests.every((request) => new URL(request.url).pathname.endsWith(`/apps/${path.appID}`)),
    ).toBe(true)
  },
)

it('Slack waits for this app and the exact OAuth flow', async () => {
  vi.useFakeTimers()
  const requests: URL[] = []
  let reads = 0
  const client = createOmnaraClient({
    baseUrl: 'https://omnara.test/api/v1',
    fetch: async (input, init) => {
      const request = new Request(input, init),
        url = new URL(request.url)
      requests.push(url)
      if (url.pathname.endsWith('/oauth/setup')) {
        expect(await request.json()).toEqual({
          client_id: 'client',
          client_secret: 'secret',
          signing_secret: 'signing',
        })
        return json(
          {
            app_id: path.appID,
            provider: 'slack',
            flow_id: id('ioaf'),
            setup_revision: 1,
            oauth_url: 'https://slack.test/authorize',
            redirect_uri: 'https://omnara.test/api/integrations/oauth/callback',
            events_url: 'https://omnara.test/events',
            actions_url: 'https://omnara.test/actions',
            expires_at: new Date(Date.now() + 60_000).toISOString(),
          },
          201,
        )
      }
      reads++
      return json({
        ...app('slack_thread'),
        last_oauth_flow_id: reads === 1 ? `ioaf_${'b'.repeat(26)}` : id('ioaf'),
      })
    },
  })
  const output = report()
  try {
    const run = runSlackIntegration({
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
    await vi.advanceTimersByTimeAsync(2100)
    await run
    expect(reads).toBe(2)
    expect(requests.every((request) => request.pathname.includes(`/apps/${path.appID}`))).toBe(true)
    expect(output.info).toHaveBeenCalledWith(`App ID: ${path.appID} (support)`)
  } finally {
    vi.useRealTimers()
  }
})

it('rejects mixed Slack creation and existing-app credentials before sending a request', async () => {
  const fetch = vi.fn()
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch })
  await expect(
    runSlackIntegration({
      client,
      path,
      apiUrl: 'https://omnara.test/api/v1',
      report: report(),
      body: zSlackBody.parse({
        app_name: 'Support',
        app_configuration_token: 'manifest-token',
        client_id: 'client',
        browser: false,
      }),
    }),
  ).rejects.toThrow('choose either')
  expect(fetch).not.toHaveBeenCalled()
})
