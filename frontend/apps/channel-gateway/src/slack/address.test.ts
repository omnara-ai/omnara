import type { ChannelResolveAddressOperation } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'
import { z } from 'zod'

import type { CoreClient } from '../core-client'
import { resolveSlackAddress } from './address'
import { SlackClient } from './client'
import { body, credentials, deferred, json, operation, slackServer } from './test-support'

const suffix = 'aaaaaaaaaaaaaaaaaaaaaaaaae'
const installation = {
  integration_app_id: `iapp_${suffix}`,
  integration_install_id: `iin_${suffix}`,
}
const room = {
  id: 'C1',
  name: 'engineering',
  is_channel: true,
  is_im: false,
  context_team_id: 'T1',
}
function context() {
  return {
    installation,
    teamId: 'T1',
    publishDefinition: vi
      .fn<CoreClient['publishDefinition']>()
      .mockImplementation((_scope, body) => Promise.resolve({ ...body, id: `cdef_${suffix}` })),
  }
}

describe('Slack address resolution', () => {
  it.each([
    { channel: room, kind: 'channel', opensReply: true },
    { channel: { id: 'G1', name: 'private', is_group: true }, kind: 'channel', opensReply: true },
    { channel: { id: 'D1', is_im: true }, kind: 'dm', opensReply: false },
    // Classify with provider facts, not the locator's prefix.
    { channel: { id: 'C1', is_im: true }, kind: 'dm', opensReply: false },
  ])(
    'resolves $kind from verified provider facts without provider writes',
    async ({ channel, kind, opensReply }) => {
      const paths: string[] = []
      const url = await slackServer((request, response) => {
        paths.push(request.url ?? '')
        expect(request.headers.authorization).toBe(`Bearer ${credentials.botToken}`)
        void body(request).then((bytes) => {
          expect(JSON.parse(bytes.toString())).toEqual({ channel: channel.id })
          json(response, z.json().parse({ ok: true, channel }))
        })
      })
      const ctx = context()
      const result = await resolveSlackAddress(
        new SlackClient(credentials.botToken, url),
        { provider_ref: channel.id },
        ctx,
        operation(),
      )
      expect(result).toMatchObject({
        definition_id: `cdef_${suffix}`,
        provider_ref: channel.id,
        provider_ref_kind: kind,
      })
      expect(result.parent).toBeUndefined()
      expect(ctx.publishDefinition).toHaveBeenCalledOnce()
      expect(ctx.publishDefinition.mock.calls[0]?.[0]).toEqual(installation)
      expect(ctx.publishDefinition.mock.calls[0]?.[1]).toMatchObject({
        implementation_key: `slack_${kind}`,
        capabilities: { creates_reply_channel: opensReply },
      })
      expect(ctx.publishDefinition.mock.calls[0]?.[2]).toBeInstanceOf(AbortSignal)
      expect(paths).toEqual(['/conversations.info'])
    },
  )

  it('proves a thread root and returns canonical provider identity with a parent descriptor', async () => {
    const requests: { path: string; input: unknown }[] = []
    const url = await slackServer((request, response) => {
      void body(request).then((bytes) => {
        requests.push({ path: request.url ?? '', input: JSON.parse(bytes.toString()) })
        if (request.url === '/conversations.info') json(response, { ok: true, channel: room })
        else
          json(response, {
            ok: true,
            messages: [{ ts: '10000000000000000001.200000', text: 'root' }],
            has_more: true,
          })
      })
    })
    const ctx = context()
    const result = await resolveSlackAddress(
      new SlackClient(credentials.botToken, url),
      {
        provider_ref: 'C1:10000000000000000001.2',
        provider_ref_kind: 'thread',
      },
      ctx,
      operation(),
    )
    expect(result).toEqual({
      definition_id: `cdef_${suffix}`,
      provider_ref: 'C1:10000000000000000001.200000',
      provider_ref_kind: 'thread',
      display_name: 'engineering',
      parent: {
        definition_id: `cdef_${suffix}`,
        provider_ref: 'C1',
        provider_ref_kind: 'channel',
        display_name: 'engineering',
      },
    })
    expect(requests).toEqual([
      { path: '/conversations.info', input: { channel: 'C1' } },
      {
        path: '/conversations.replies',
        input: { channel: 'C1', ts: '10000000000000000001.2', limit: 1 },
      },
    ])
    expect(
      ctx.publishDefinition.mock.calls.map(([, definition]) => definition.implementation_key),
    ).toEqual(['slack_channel', 'slack_thread'])
  })

  it.each([
    { provider_ref: 'https://slack.example/C1' },
    { provider_ref: 'C1/100.000001' },
    { provider_ref: ' C1' },
    { provider_ref: 'U1' },
    { provider_ref: 'C1:' },
    { provider_ref: 'C1:1.000001:extra' },
    { provider_ref: 'C1:1.1234567' },
  ])('rejects malformed native locators before provider I/O: %j', async (input) => {
    const api = vi.spyOn(SlackClient.prototype, 'api')
    const ctx = context()
    try {
      await expect(
        resolveSlackAddress(new SlackClient(credentials.botToken), input, ctx, operation()),
      ).rejects.toMatchObject({ code: 'invalid_address' })
      expect(api).not.toHaveBeenCalled()
      expect(ctx.publishDefinition).not.toHaveBeenCalled()
    } finally {
      api.mockRestore()
    }
  })

  it.each([
    {
      channel: { ...room, id: 'COTHER' },
      input: { provider_ref: 'C1' },
      code: 'address_unavailable',
    },
    {
      channel: { ...room, context_team_id: 'TOTHER' },
      input: { provider_ref: 'C1' },
      code: 'address_unavailable',
    },
    {
      channel: { name: 'missing identity' },
      input: { provider_ref: 'C1' },
      code: 'permanent_failure',
    },
    { channel: { id: 'C1' }, input: { provider_ref: 'C1' }, code: 'permanent_failure' },
    {
      channel: { id: 'G1', is_mpim: true, is_group: true },
      input: { provider_ref: 'G1' },
      code: 'unsupported_address',
    },
    {
      channel: { id: 'D1', is_im: true },
      input: { provider_ref: 'D1:100.000001' },
      code: 'unsupported_address',
    },
    {
      channel: room,
      input: { provider_ref: 'C1', provider_ref_kind: 'dm' },
      code: 'invalid_address',
    },
  ])(
    'rejects identity/kind mismatches without publishing a definition',
    async ({ channel, input, code }) => {
      let calls = 0
      const url = await slackServer((_request, response) => {
        calls++
        json(response, z.json().parse({ ok: true, channel }))
      })
      const ctx = context()
      await expect(
        resolveSlackAddress(new SlackClient(credentials.botToken, url), input, ctx, operation()),
      ).rejects.toMatchObject({ code })
      expect(calls).toBe(1)
      expect(ctx.publishDefinition).not.toHaveBeenCalled()
    },
  )

  it.each([
    { messages: [] },
    { messages: [{ ts: '99.000001' }] },
    { messages: [{ ts: '100.000001', channel: 'COTHER' }] },
    { messages: [{ ts: '100.000001', thread_ts: '99.000001' }] },
  ])(
    'rejects a missing or different root rather than silently resolving another thread',
    async ({ messages }) => {
      const url = await slackServer((request, response) => {
        json(
          response,
          z
            .json()
            .parse(
              request.url === '/conversations.info'
                ? { ok: true, channel: room }
                : { ok: true, messages },
            ),
        )
      })
      const ctx = context()
      await expect(
        resolveSlackAddress(
          new SlackClient(credentials.botToken, url),
          { provider_ref: 'C1:100.000001' },
          ctx,
          operation(),
        ),
      ).rejects.toMatchObject({ code: 'address_unavailable' })
      expect(ctx.publishDefinition).not.toHaveBeenCalled()
    },
  )

  it('uses the existing four-attempt read budget and never publishes after failed provider reads', async () => {
    let calls = 0
    const url = await slackServer((_request, response) => {
      calls++
      json(response, { ok: false }, 503)
    })
    const ctx = context()
    await expect(
      resolveSlackAddress(
        new SlackClient(credentials.botToken, url),
        { provider_ref: 'C1' },
        ctx,
        operation(),
      ),
    ).rejects.toMatchObject({ code: 'retries_exhausted', attempts: 4 })
    expect(calls).toBe(4)
    expect(ctx.publishDefinition).not.toHaveBeenCalled()
  })

  it('uses the same bounded deadline for definition publication', async () => {
    const url = await slackServer((_request, response) => {
      json(response, { ok: true, channel: room })
    })
    const ctx = context()
    const aborted = deferred()
    ctx.publishDefinition.mockImplementation(
      (_scope, _body, signal) =>
        new Promise((_resolve, reject) => {
          signal.addEventListener(
            'abort',
            () => {
              aborted.resolve()
              reject(new Error('definition request aborted'))
            },
            { once: true },
          )
        }),
    )
    const input: ChannelResolveAddressOperation = { provider_ref: 'C1' }
    await expect(
      resolveSlackAddress(new SlackClient(credentials.botToken, url), input, ctx, operation(100)),
    ).rejects.toMatchObject({ code: 'deadline_exceeded' })
    await aborted.promise
    expect(ctx.publishDefinition).toHaveBeenCalledOnce()
  })
})
