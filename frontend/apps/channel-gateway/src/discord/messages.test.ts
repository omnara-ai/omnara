import { describe, expect, it, vi } from 'vitest'

import { DiscordClient } from './client'
import { sendDiscordMessage } from './messages'
import {
  artifact,
  body,
  config,
  deferred,
  destination,
  identity,
  json,
  message,
  operation,
  room,
  server,
  thread,
  threadDestination,
} from './test-support'

const grants = { receive: true, read: false, send: true }
describe('Discord message publication', () => {
  it('publishes one root and creates its named public thread only with explicit grants', async () => {
    const posts: { path: string; input: unknown }[] = []
    const url = await server((request, response) => {
      if (identity(request, response)) return
      if (request.method === 'GET') {
        json(response, room)
        return
      }
      void body(request).then((bytes) => {
        posts.push({ path: request.url ?? '', input: JSON.parse(bytes.toString()) })
        json(response, request.url?.endsWith('/threads') ? thread : message)
      })
    })
    const result = await sendDiscordMessage(
      new DiscordClient(config, url),
      {
        destination,
        message: { text: 'hello' },
        params: { thread_name: 'Daily report' },
        reply_channel_grants: grants,
      },
      [],
      operation(),
    )
    expect(result.message.id).toBe(message.id)
    expect(result.thread).toEqual(thread)
    expect(posts).toEqual([
      {
        path: `/channels/${room.id}/messages`,
        input: { content: 'hello', allowed_mentions: { parse: [] } },
      },
      {
        path: `/channels/${room.id}/messages/${message.id}/threads`,
        input: { name: 'Daily report' },
      },
    ])
  })

  it('keeps text and artifacts in one publication when the thread step is retried', async () => {
    const file = await artifact('report contents')
    const open = vi.spyOn(file, 'open')
    let roots = 0
    let threads = 0
    const url = await server((request, response) => {
      if (identity(request, response)) return
      if (request.method === 'GET') {
        json(response, room)
        return
      }
      void body(request).then((bytes) => {
        if (request.url?.endsWith('/threads')) {
          threads++
          if (threads === 1) json(response, { retry_after: 0 }, 429)
          else json(response, thread)
          return
        }
        roots++
        expect(request.headers['content-type']).toMatch(/^multipart\/form-data;/)
        expect(bytes.toString()).toContain('"content":"Daily report"')
        expect(bytes.toString()).toContain('name="files[0]"; filename="notes.txt"')
        expect(bytes.toString()).toContain('report contents')
        json(response, { ...message, content: 'Daily report', attachments: [{ id: room.id }] })
      })
    })
    const result = await sendDiscordMessage(
      new DiscordClient(config, url),
      {
        destination,
        message: { text: 'Daily report', artifact_ids: [file.id] },
        params: {},
        reply_channel_grants: grants,
      },
      [file],
      operation(),
    )
    expect(result.message.attachments).toEqual([{ id: room.id }])
    expect(result.thread?.id).toBe(thread.id)
    expect(result.continuationError).toBeUndefined()
    expect(roots).toBe(1)
    expect(threads).toBe(2)
    expect(open).toHaveBeenCalledOnce()
  })
  it.each([destination, threadDestination])(
    'sends directly to $provider_ref_kind without inventing delegation',
    async (target) => {
      const posts: string[] = []
      const url = await server((request, response) => {
        if (identity(request, response)) return
        if (request.method === 'GET') {
          json(response, request.url === `/channels/${thread.id}` ? thread : room)
          return
        }
        posts.push(request.url ?? '')
        void body(request).then(() => {
          json(response, { ...message, channel_id: target.provider_ref })
        })
      })
      const result = await sendDiscordMessage(
        new DiscordClient(config, url),
        { destination: target, message: { text: 'hello' }, params: {} },
        [],
        operation(),
      )
      expect(result.thread).toBeUndefined()
      expect(result.continuationError).toBeUndefined()
      expect(posts).toEqual([`/channels/${target.provider_ref}/messages`])
    },
  )
  it.each([403, 500, 'wrong-parent'] as const)(
    'preserves the known root and never reposts when continuation fails: %s',
    async (failure) => {
      let roots = 0
      let threads = 0
      const url = await server((request, response) => {
        if (identity(request, response)) return
        if (request.method === 'GET') {
          json(response, room)
          return
        }
        void body(request).then(() => {
          if (request.url?.endsWith('/threads')) {
            threads++
            if (failure === 'wrong-parent')
              json(response, { ...thread, parent_id: '777777777777777777' })
            else json(response, { message: 'private provider detail' }, failure)
          } else {
            roots++
            json(response, message)
          }
        })
      })
      const result = await sendDiscordMessage(
        new DiscordClient(config, url),
        {
          destination,
          message: { text: 'hello' },
          params: {},
          reply_channel_grants: grants,
        },
        [],
        operation(),
      )
      expect(result.message.id).toBe(message.id)
      expect(result.thread).toBeUndefined()
      expect(result.continuationError?.code).toBe('reply_channel_unavailable')
      expect(JSON.stringify(result)).not.toContain('private provider detail')
      expect(roots).toBe(1)
      expect(threads).toBe(1)
    },
  )
  it('retries only the definitely rejected thread step within the same attempt budget', async () => {
    let roots = 0
    let threads = 0
    const url = await server((request, response) => {
      if (identity(request, response)) return
      if (request.method === 'GET') {
        json(response, room)
        return
      }
      void body(request).then(() => {
        if (request.url?.endsWith('/threads')) {
          threads++
          if (threads < 4) json(response, { retry_after: 0 }, 429)
          else json(response, thread)
        } else {
          roots++
          json(response, message)
        }
      })
    })
    const result = await sendDiscordMessage(
      new DiscordClient(config, url),
      {
        destination,
        message: { text: 'hello' },
        params: {},
        reply_channel_grants: grants,
      },
      [],
      operation(),
    )
    expect(result.thread?.id).toBe(thread.id)
    expect(roots).toBe(1)
    expect(threads).toBe(4)
  })
  it('does not retry an ambiguous root publication or create a thread after it', async () => {
    let posts = 0
    const url = await server((request, response) => {
      if (identity(request, response)) return
      if (request.method === 'GET') {
        json(response, room)
        return
      }
      posts++
      void body(request).then(() => request.socket.destroy())
    })
    await expect(
      sendDiscordMessage(
        new DiscordClient(config, url),
        {
          destination,
          message: { text: 'hello' },
          params: {},
          reply_channel_grants: grants,
        },
        [],
        operation(),
      ),
    ).rejects.toMatchObject({ outcomeUnknown: true, attempts: 1 })
    expect(posts).toBe(1)
  })

  it('retains publication when the thread response stalls until the deadline', async () => {
    const closed = deferred()
    let roots = 0
    const url = await server((request, response) => {
      if (identity(request, response)) return
      if (request.method === 'GET') {
        json(response, room)
        return
      }
      void body(request).then(() => {
        if (request.url?.endsWith('/threads')) {
          response.writeHead(200, { 'content-type': 'application/json' })
          response.write('{')
          response.once('close', closed.resolve)
        } else {
          roots++
          json(response, message)
        }
      })
    })
    const result = await sendDiscordMessage(
      new DiscordClient(config, url),
      {
        destination,
        message: { text: 'hello' },
        params: {},
        reply_channel_grants: grants,
      },
      [],
      operation(200),
    )
    expect(result.message.id).toBe(message.id)
    expect(result.continuationError?.code).toBe('reply_channel_unavailable')
    expect(roots).toBe(1)
    await closed.promise
  })
  it.each([
    { message: { text: 'x'.repeat(2001) }, params: {} },
    { message: { text: 'hello' }, params: { thread_name: 'not authorized' } },
    { message: { text: 'hello' }, params: { unknown: true } },
    { message: { text: 'hello', artifact_ids: ['missing'] }, params: {} },
    {
      message: { text: 'hello' },
      params: {},
      reply_channel_grants: { receive: false, read: false, send: false },
    },
  ])('rejects invalid input before all provider I/O', async (input) => {
    let requests = 0
    const url = await server((_request, response) => {
      requests++
      json(response, {})
    })
    await expect(
      sendDiscordMessage(
        new DiscordClient(config, url),
        { ...input, destination },
        [],
        operation(),
      ),
    ).rejects.toThrow()
    expect(requests).toBe(0)
  })
  it('rejects missing/mismatched artifact declarations before identity reads', async () => {
    const file = await artifact()
    let requests = 0
    const url = await server((_request, response) => {
      requests++
      json(response, {})
    })
    await expect(
      sendDiscordMessage(
        new DiscordClient(config, url),
        { destination, message: { text: 'hello' }, params: {} },
        [file],
        operation(),
      ),
    ).rejects.toMatchObject({ code: 'artifact_mismatch' })
    expect(requests).toBe(0)
  })
})
