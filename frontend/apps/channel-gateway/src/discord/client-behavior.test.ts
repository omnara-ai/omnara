import { describe, expect, it } from 'vitest'

import { retryOperation } from '../operations/retry'
import { DiscordClient } from './client'
import {
  attempt,
  body,
  config,
  deferred,
  json,
  message,
  operation,
  room,
  server,
  thread,
} from './test-support'

describe('Discord client behavior', () => {
  it('preserves raw formatting and large IDs in a full history page', async () => {
    const page = Array.from({ length: 100 }, (_, index) => ({
      ...message,
      id: String(900719925474099399n - BigInt(index)),
      content: '*x* '.repeat(1000),
    }))
    const url = await server((request, response) => {
      expect(request.url).toBe(`/channels/${room.id}/messages?limit=100&before=${message.id}`)
      json(response, page)
    })
    const result = await new DiscordClient(config, url).getMessages(
      room.id,
      100,
      message.id,
      attempt(),
    )
    expect(result).toEqual(page)
  })

  it.each([room.id, thread.id])(
    'preserves code and native text with safe mentions in %s',
    async (id) => {
      const content =
        '  @everyone @here @Ada <@123456789012345678> <@&123456789012345679>\n' +
        '```ts\nconst emoji = ":smile:"; // @name **raw**\n```\n' +
        '`<@123456789012345678> :thumbsup:` **bold** \\*literal\\*  '
      const url = await server((request, response) => {
        expect(request.url).toBe(`/channels/${id}/messages`)
        void body(request).then((bytes) => {
          expect(JSON.parse(bytes.toString())).toEqual({
            content,
            allowed_mentions: { parse: [] },
            attachments: [],
          })
          json(response, { ...message, channel_id: id, content })
        })
      })
      const result = await new DiscordClient(config, url).createMessage(id, content, [], attempt())
      expect(result.content).toBe(content)
      expect(result.id).toBe(message.id)
    },
  )

  it.each([null, [], {}, { ...message, id: 123 }].map((value) => ({ value })))(
    'keeps malformed successful post responses ambiguous: $value',
    async ({ value }) => {
      let calls = 0
      const url = await server((_request, response) => {
        calls++
        json(response, value)
      })
      await expect(
        retryOperation(operation(), (context) =>
          new DiscordClient(config, url).createMessage(room.id, 'hello', [], context),
        ),
      ).rejects.toMatchObject({ outcomeUnknown: true, attempts: 1 })
      expect(calls).toBe(1)
    },
  )

  it('retries an explicitly rejected post once with the provider delay', async () => {
    const calls: number[] = []
    const url = await server((_request, response) => {
      calls.push(Date.now())
      if (calls.length === 1) json(response, { retry_after: 0.3 }, 429)
      else json(response, message)
    })
    const client = new DiscordClient(config, url)
    expect(
      (
        await retryOperation(operation(), (context) =>
          client.createMessage(room.id, 'hello', [], context),
        )
      ).id,
    ).toBe(message.id)
    expect(calls).toHaveLength(2)
    expect((calls[1] ?? 0) - (calls[0] ?? 0)).toBeGreaterThanOrEqual(300)
  })

  it.each(['channel', 'history'] as const)(
    'classifies malformed %s JSON before consumption',
    async (kind) => {
      const url = await server((_request, response) => {
        json(response, null)
      })
      const client = new DiscordClient(config, url)
      await expect(
        kind === 'channel'
          ? client.getChannel(room.id, attempt())
          : client.getMessages(room.id, 10, undefined, attempt()),
      ).rejects.toMatchObject({ code: 'invalid_response', outcomeUnknown: false, retryable: false })
    },
  )

  it.each(['post', 'history'] as const)('caps actual response bytes for %s', async (kind) => {
    const url = await server((_request, response) => {
      const oversized = { ...message, ignored: 'x'.repeat(1024 * 1024) }
      json(response, kind === 'post' ? oversized : [oversized])
    })
    const client = new DiscordClient(config, url)
    await expect(
      kind === 'post'
        ? client.createMessage(room.id, 'hello', [], attempt())
        : client.getMessages(room.id, 10, undefined, attempt()),
    ).rejects.toMatchObject({
      code: 'provider_unavailable',
      outcomeUnknown: kind === 'post',
      retryable: kind !== 'post',
    })
  })

  it.each(['cancel', 'deadline'] as const)(
    'isolates a post %s from a concurrent history read on the same client',
    async (stop) => {
      const started = deferred()
      const closed = deferred()
      const url = await server((request, response) => {
        if (request.method === 'GET') {
          json(response, [message])
          return
        }
        response.writeHead(200, { 'content-type': 'application/json' })
        response.write('{')
        response.once('close', closed.resolve)
        started.resolve()
      })
      const controller = new AbortController()
      const client = new DiscordClient(config, url)
      const pending = client.createMessage(room.id, 'hello', [], {
        ...attempt(stop === 'deadline' ? 500 : 5000),
        signal: controller.signal,
      })
      const rejected = expect(pending).rejects.toMatchObject({ outcomeUnknown: true })
      await started.promise
      if (stop === 'cancel') controller.abort()
      const page = await client.getMessages(room.id, 10, undefined, attempt())
      expect(page.map((item) => item.id)).toEqual([message.id])
      await rejected
      await closed.promise
    },
  )

  it('keeps tokens, guild IDs and API targets isolated across concurrent clients', async () => {
    const other = { ...config, guildID: '999999999999999999', botToken: 'other-local-token' }
    const paths: string[] = []
    const url = await server((request, response) => {
      paths.push(request.url ?? '')
      json(response, message)
    })
    const otherURL = await server((request, response) => {
      paths.push(request.url ?? '')
      json(response, { ...message, channel_id: thread.id, guild_id: other.guildID })
    }, other.botToken)
    const [first, second] = await Promise.all([
      new DiscordClient(config, url).createMessage(room.id, 'first', [], attempt()),
      new DiscordClient(other, otherURL).createMessage(thread.id, 'second', [], attempt()),
    ])
    expect(first.guild_id).toBe(config.guildID)
    expect(second.guild_id).toBe(other.guildID)
    expect(paths.sort()).toEqual([
      `/channels/${room.id}/messages`,
      `/channels/${thread.id}/messages`,
    ])
  })
})
