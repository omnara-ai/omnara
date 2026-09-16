import { describe, expect, it, vi } from 'vitest'

import { retryOperation } from '../operation-retry'
import { DiscordClient, discordRequestBytes } from './client'
import {
  artifact,
  attempt,
  body,
  config,
  deferred,
  identity,
  json,
  message,
  operation,
  room,
  server,
  thread,
  user,
} from './test-support'

describe('Discord bounded REST client', () => {
  it('verifies application and bot separately, then reuses the verified immutable token', async () => {
    const paths: string[] = []
    const url = await server((request, response) => {
      paths.push(request.url ?? '')
      identity(request, response)
    })
    const client = new DiscordClient(config, url)
    await client.verifyIdentity(attempt())
    await client.verifyIdentity(attempt())
    expect(paths).toEqual(['/applications/@me', '/users/@me'])
  })
  it.each(['application', 'bot', 'human'])(
    'rejects mismatched token identity: %s',
    async (wrong) => {
      const url = await server((request, response) => {
        if (request.url === '/applications/@me')
          json(response, { id: wrong === 'application' ? room.id : config.applicationID })
        else
          json(response, {
            ...user,
            id: wrong === 'bot' ? room.id : user.id,
            bot: wrong !== 'human',
          })
      })
      await expect(new DiscordClient(config, url).verifyIdentity(attempt())).rejects.toMatchObject({
        code: 'identity_mismatch',
      })
    },
  )
  it.each([
    'http://provider.example/',
    'https://user:password@discord.com/',
    'https://discord.com/?token=x',
  ])('rejects unsafe deployment URL %s', (url) => {
    expect(() => new DiscordClient(config, url)).toThrow('invalid_configuration')
  })
  it('rejects URL-shaped resource IDs without making a request', async () => {
    let calls = 0
    const url = await server((_request, response) => {
      calls++
      json(response, {})
    })
    await expect(
      new DiscordClient(config, url).getChannel('https://other.example/', attempt()),
    ).rejects.toMatchObject({ code: 'invalid_destination' })
    expect(calls).toBe(0)
  })
  it('publishes text and streamed files in one native message', async () => {
    const file = await artifact('original contents')
    let received = Buffer.alloc(0)
    let calls = 0
    const url = await server((request, response) => {
      calls++
      expect(request.url).toBe(`/channels/${room.id}/messages`)
      expect(request.headers['content-type']).toMatch(/^multipart\/form-data; boundary=/)
      void body(request).then((bytes) => {
        received = bytes
        expect(bytes.length).toBe(Number(request.headers['content-length']))
        json(response, { ...message, attachments: [{ id: room.id }] })
      })
    })
    const client = new DiscordClient(config, url)
    expect((await client.createMessage(room.id, 'hello', [file], attempt())).id).toBe(message.id)
    expect(received.toString()).toContain('name="files[0]"; filename="notes.txt"')
    expect(received.toString()).toContain('original contents')
    expect(received.toString()).toContain('"content":"hello"')
    expect(received.toString()).toContain('"allowed_mentions":{"parse":[]}')
    expect(calls).toBe(1)
  })
  it('uses a named public thread endpoint without creating a second message', async () => {
    const url = await server((request, response) => {
      expect(request.url).toBe(`/channels/${room.id}/messages/${message.id}/threads`)
      void body(request).then((bytes) => {
        expect(JSON.parse(bytes.toString())).toEqual({ name: 'Review' })
        json(response, thread)
      })
    })
    expect(
      await new DiscordClient(config, url).createThread(room.id, message.id, 'Review', attempt()),
    ).toEqual(thread)
  })
  it.each(['x'.repeat(2001), '😀'.repeat(1001), '\ud800', ''])(
    'rejects oversized/invalid text before I/O',
    async (text) => {
      let calls = 0
      const url = await server((_request, response) => {
        calls++
        json(response, {})
      })
      await expect(
        new DiscordClient(config, url).createMessage(room.id, text, [], attempt()),
      ).rejects.toMatchObject({ code: 'invalid_message' })
      expect(calls).toBe(0)
    },
  )
  it('accepts the text boundary without silent truncation', async () => {
    const text = 'x'.repeat(2000)
    const url = await server((request, response) => {
      void body(request).then((bytes) => {
        expect(JSON.parse(bytes.toString())).toMatchObject({ content: text })
        json(response, { ...message, content: text })
      })
    })
    await new DiscordClient(config, url).createMessage(room.id, text, [], attempt())
  })
  it('includes multipart overhead in preflight and never opens an oversized artifact', async () => {
    const file = await artifact()
    file.sizeBytes = discordRequestBytes
    const open = vi.spyOn(file, 'open')
    let requests = 0
    const url = await server((_request, response) => {
      requests++
      json(response, {})
    })
    await expect(
      new DiscordClient(config, url).createMessage(room.id, undefined, [file], attempt()),
    ).rejects.toMatchObject({ code: 'request_too_large' })
    expect(requests).toBe(0)
    expect(open).not.toHaveBeenCalled()
  })
  it('checks streamed bytes when declared size understates the artifact', async () => {
    const file = await artifact('longer than declared')
    file.sizeBytes = 1
    let published = 0
    const url = await server((request, response) => {
      void body(request).then(
        () => {
          published++
          json(response, message)
        },
        () => undefined,
      )
    })
    await expect(
      new DiscordClient(config, url).createMessage(room.id, undefined, [file], attempt()),
    ).rejects.toMatchObject({ outcomeUnknown: true })
    expect(published).toBe(0)
  })

  it('closes the source file when a blocked upload reaches its deadline', async () => {
    const file = await artifact(Buffer.alloc(8 * 1024 * 1024))
    const opened = deferred()
    const closed = deferred()
    const originalOpen = file.open
    file.open = () => {
      const stream = originalOpen()
      stream.once('close', closed.resolve)
      opened.resolve()
      return stream
    }
    const url = await server((request) => {
      request.pause()
    })
    const pending = new DiscordClient(config, url).createMessage(
      room.id,
      undefined,
      [file],
      attempt(200),
    )
    await opened.promise
    await expect(pending).rejects.toMatchObject({ outcomeUnknown: true })
    await closed.promise
  })
  it('obeys fractional provider rate guidance within one four-attempt budget', async () => {
    const calls: number[] = []
    const url = await server((_request, response) => {
      calls.push(Date.now())
      if (calls.length < 4) json(response, { retry_after: 0.01 }, 429, { 'retry-after': '0' })
      else json(response, room)
    })
    const client = new DiscordClient(config, url)
    const result = await retryOperation({ ...operation(), idempotent: true }, (context) =>
      client.getChannel(room.id, context),
    )
    expect(result.id).toBe(room.id)
    expect(calls.length).toBe(4)
    for (let i = 1; i < calls.length; i++)
      expect((calls[i] ?? 0) - (calls[i - 1] ?? 0)).toBeGreaterThanOrEqual(10)
  })
  it.each([400, 403, 429, 500, 302])(
    'does not hide a mutation retry after status %s',
    async (status) => {
      let calls = 0
      const url = await server((_request, response) => {
        calls++
        json(response, { private: 'provider diagnostic' }, status, { location: '/must-not-follow' })
      })
      const client = new DiscordClient(config, url)
      await expect(
        retryOperation(operation(), (context) =>
          client.createMessage(room.id, 'hello', [], context),
        ),
      ).rejects.toMatchObject({ attempts: 1, outcomeUnknown: status === 500 || status === 302 })
      expect(calls).toBe(1)
    },
  )
  it.each(['{"id":"1","i\\u0064":"2"}', '{} {}', '{"id":111111111111111111}', 'not JSON'])(
    'rejects ambiguous/invalid provider JSON: %s',
    async (raw) => {
      const url = await server((_request, response) => {
        response.end(raw)
      })
      await expect(new DiscordClient(config, url).getChannel(room.id, attempt())).rejects.toThrow()
    },
  )
  it('aborts an actual stalled response at its deadline', async () => {
    const started = deferred()
    const closed = deferred()
    const url = await server((_request, response) => {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.write('{')
      response.once('close', closed.resolve)
      started.resolve()
    })
    const pending = new DiscordClient(config, url).getChannel(room.id, attempt(150))
    await started.promise
    await expect(pending).rejects.toThrow()
    await closed.promise
  })
})
