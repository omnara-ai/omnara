import type { JsonBody } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import {
  discordAPIURL,
  discordIntents,
  fetchDiscordGatewayInfo,
  inspectDiscordApplication,
  validateDiscordGatewayURL,
} from './bootstrap'
import { app, attempt, config, json, operation, server, user } from './test-support'

const application = { id: config.applicationID, flags: 1 << 19 }
const gateway = {
  url: 'wss://gateway.discord.gg',
  shards: 4,
  session_start_limit: { total: 1000, remaining: 23, reset_after: 60_000, max_concurrency: 2 },
}
async function setup(values: { application?: JsonBody; user?: JsonBody; gateway?: JsonBody } = {}) {
  const calls: string[] = []
  const apiUrl = await server((request, response) => {
    expect(request.method).toBe('GET')
    calls.push(request.url ?? '')
    switch (request.url) {
      case '/applications/@me':
        json(response, values.application ?? application)
        break
      case '/users/@me':
        json(response, values.user ?? user)
        break
      case '/gateway/bot':
        json(response, values.gateway ?? gateway)
        break
      default:
        json(response, {}, 404)
    }
  })
  return { apiUrl, calls }
}

describe('Discord runtime app inspection', () => {
  it.each([1 << 18, 1 << 19])(
    'verifies intent flag %s and distinct app/bot identity',
    async (flags) => {
      const { apiUrl, calls } = await setup({ application: { ...application, flags } })
      const result = await inspectDiscordApplication(app, operation(), apiUrl)
      expect(result).toEqual({
        applicationID: config.applicationID,
        botUserID: config.botUserID,
        botToken: config.botToken,
      })
      expect(calls).toEqual(['/applications/@me', '/users/@me'])
      expect(discordIntents).toBe(33281)
    },
  )

  it('requires Message Content even though a bot can receive readable mentions without it', async () => {
    // Discord's mention exemption makes a successful mention an insufficient
    // setup check: ordinary replies can still arrive with content/attachments empty.
    const { apiUrl, calls } = await setup({ application: { ...application, flags: 0 } })
    await expect(inspectDiscordApplication(app, operation(), apiUrl)).rejects.toMatchObject({
      code: 'message_content_intent_required',
    })
    expect(calls).toEqual(['/applications/@me'])
  })

  it.each([
    { application: { ...application, id: config.botUserID } },
    { application: { ...application, id: Number(config.applicationID) } },
    { application: { id: config.applicationID } },
    { user: { ...user, bot: false } },
  ])('rejects unverified app/intent/bot facts without gateway lookup', async (values) => {
    const { apiUrl, calls } = await setup(values)
    await expect(inspectDiscordApplication(app, operation(), apiUrl)).rejects.toThrow()
    expect(calls).not.toContain('/gateway/bot')
  })

  it('returns exhausted provider truth without inventing credits or starting sockets', async () => {
    const limits = { ...gateway.session_start_limit, remaining: 0, reset_after: 0 }
    const { apiUrl } = await setup({ gateway: { ...gateway, session_start_limit: limits } })
    expect(
      await fetchDiscordGatewayInfo(config.botToken, discordAPIURL(apiUrl), attempt()),
    ).toMatchObject({
      session_start_limit: limits,
    })
  })

  it.each([
    { shards: 0 },
    { shards: 1.5 },
    { session_start_limit: { ...gateway.session_start_limit, remaining: 1001 } },
    { session_start_limit: { ...gateway.session_start_limit, remaining: -1 } },
    { session_start_limit: { ...gateway.session_start_limit, reset_after: -1 } },
    { session_start_limit: { ...gateway.session_start_limit, max_concurrency: 0 } },
  ])('rejects malformed shard/session limits', async (invalid) => {
    const { apiUrl } = await setup({ gateway: { ...gateway, ...invalid } })
    await expect(
      fetchDiscordGatewayInfo(config.botToken, discordAPIURL(apiUrl), attempt()),
    ).rejects.toThrow('invalid_response')
  })

  it('shares the initial plus three retry budget across identity reads', async () => {
    let reads = 0
    const apiUrl = await server((request, response) => {
      if (request.url === '/applications/@me') json(response, application)
      else {
        reads++
        json(response, { retry_after: 0 }, 429)
      }
    })
    await expect(inspectDiscordApplication(app, operation(), apiUrl)).rejects.toMatchObject({
      code: 'retries_exhausted',
      attempts: 4,
    })
    expect(reads).toBe(4)
  })

  it('cancels a stalled bootstrap response through the actual fetch signal', async () => {
    const controller = new AbortController()
    const apiUrl = await server((_request, response) => {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.write('{')
      controller.abort()
    })
    await expect(
      inspectDiscordApplication(app, { ...operation(), signal: controller.signal }, apiUrl),
    ).rejects.toMatchObject({ code: 'canceled' })
  })

  it('rejects duplicate provider fields before using bootstrap facts', async () => {
    const apiUrl = await server((_request, response) => {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end(`{"id":"${config.applicationID}","flags":524288,"fl\\u0061gs":0}`)
    })
    await expect(inspectDiscordApplication(app, operation(100), apiUrl)).rejects.toThrow()
  })
})

describe('Discord trusted runtime endpoints', () => {
  it.each([
    'https://evil.test/',
    'wss://gateway.discord.gg.evil.test',
    'ws://gateway.discord.gg',
    'wss://user:secret@gateway.discord.gg',
    'wss://gateway.discord.gg/?token=private',
  ])('rejects provider-selected endpoint %s', (value) => {
    expect(() => {
      validateDiscordGatewayURL(value, discordAPIURL())
    }).toThrow('invalid_gateway_url')
  })
  it('allows official regional resume hosts and explicit local test endpoints only', () => {
    expect(() => {
      validateDiscordGatewayURL('wss://gateway-us-east1-b.discord.gg', discordAPIURL())
    }).not.toThrow()
    expect(() => {
      validateDiscordGatewayURL('ws://127.0.0.1:1234', discordAPIURL('http://127.0.0.1:5678/'))
    }).not.toThrow()
    expect(() => {
      validateDiscordGatewayURL('ws://127.0.0.1:1234', discordAPIURL())
    }).toThrow()
  })
})
