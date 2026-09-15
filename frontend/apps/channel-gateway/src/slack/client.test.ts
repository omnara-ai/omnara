import { once } from 'node:events'
import { Readable } from 'node:stream'

import { describe, expect, it } from 'vitest'

import { SlackClient } from './client'
import { attempt, body, credentials, deferred, json, slackServer } from './test-support'

describe('SlackClient', () => {
  it('authenticates an API request and returns no credential in provider diagnostics', async () => {
    let calls = 0
    let authorization: string | undefined
    const url = await slackServer((request, response) => {
      calls++
      authorization = request.headers.authorization
      json(response, { ok: false, error: credentials.botToken })
    })
    await expect(
      new SlackClient(credentials.botToken, url).api(
        'chat.postMessage',
        {
          channel: 'C1',
          text: 'hi',
        },
        attempt(),
      ),
    ).rejects.toMatchObject({ code: 'provider_rejected', outcomeUnknown: true })
    expect(calls).toBe(1)
    expect(authorization).toBe(`Bearer ${credentials.botToken}`)
  })

  it('does not follow redirects with the bot credential', async () => {
    let redirected = 0
    const other = await slackServer((_request, response) => {
      redirected++
      json(response, { ok: true })
    })
    const url = await slackServer((_request, response) => {
      response.writeHead(307, { location: other })
      response.end()
    })
    await expect(
      new SlackClient(credentials.botToken, url).api(
        'conversations.history',
        { channel: 'C1' },
        attempt(),
      ),
    ).rejects.toMatchObject({ code: 'http_rejected' })
    expect(redirected).toBe(0)
  })

  it('surfaces Retry-After without an SDK retry', async () => {
    let calls = 0
    const url = await slackServer((_request, response) => {
      calls++
      json(response, { ok: false }, 429, { 'retry-after': '60' })
    })
    await expect(
      new SlackClient(credentials.botToken, url).api(
        'chat.postMessage',
        { channel: 'C1', text: 'hi' },
        attempt(),
      ),
    ).rejects.toMatchObject({
      code: 'ratelimited',
      retryAfterMs: 60_000,
      retryable: true,
      outcomeUnknown: false,
    })
    expect(calls).toBe(1)
  })

  it('aborts the real response read without destroying another request on the client', async () => {
    const started = deferred()
    const closed = deferred()
    const url = await slackServer((request, response) => {
      if (request.url === '/conversations.replies') {
        response.writeHead(200, { 'content-type': 'application/json' })
        response.write('{"ok":true,"messages":[')
        response.once('close', () => {
          closed.resolve()
        })
        started.resolve()
      } else json(response, { ok: true, messages: [] })
    })
    const client = new SlackClient(credentials.botToken, url)
    const controller = new AbortController()
    const pending = client.api(
      'conversations.replies',
      { channel: 'C1', ts: '100.000001' },
      attempt(controller.signal),
    )
    const rejected = expect(pending).rejects.toMatchObject({ code: 'transport_failed' })
    await started.promise
    const parallel = client.api('conversations.history', { channel: 'C1' }, attempt())
    controller.abort()
    await rejected
    await closed.promise
    expect(await parallel).toEqual({ messages: [] })
    expect(await client.api('conversations.history', { channel: 'C1' }, attempt())).toEqual({
      messages: [],
    })
  })

  it('enforces a real deadline while waiting for response headers', async () => {
    const closed = deferred()
    const url = await slackServer((_request, response) => {
      response.once('close', () => {
        closed.resolve()
      })
    })
    await expect(
      new SlackClient(credentials.botToken, url).api(
        'chat.postMessage',
        { channel: 'C1', text: 'hi' },
        attempt(undefined, 100),
      ),
    ).rejects.toMatchObject({ outcomeUnknown: true })
    await closed.promise
  })

  it('bounds response bodies and treats malformed publication responses as unknown', async () => {
    const url = await slackServer((_request, response) => {
      response.end('x'.repeat(1024 * 1024 + 1))
    })
    await expect(
      new SlackClient(credentials.botToken, url).api(
        'chat.postMessage',
        { channel: 'C1', text: 'hi' },
        attempt(),
      ),
    ).rejects.toMatchObject({ code: 'response_too_large', outcomeUnknown: true })
    const invalid = await slackServer((_request, response) => {
      response.end('{not json')
    })
    await expect(
      new SlackClient(credentials.botToken, invalid).api(
        'chat.postMessage',
        { channel: 'C1', text: 'hi' },
        attempt(),
      ),
    ).rejects.toMatchObject({ code: 'invalid_response', outcomeUnknown: true })
  })

  it('streams uploads without a bot token and checks exact artifact size', async () => {
    let authorization: string | undefined
    let received: Buffer | undefined
    const url = await slackServer((request, response) => {
      authorization = request.headers.authorization
      void body(request).then(
        (bytes) => {
          received = bytes
          response.end('OK')
        },
        () => response.end(),
      )
    })
    const client = new SlackClient(credentials.botToken, url)
    await client.upload(
      `${url}/upload`,
      { filename: 'a.txt', sizeBytes: 3, open: () => Readable.from(['a', 'bc']) },
      attempt(),
    )
    expect(authorization).toBeUndefined()
    expect(received?.toString()).toBe('abc')
    await expect(
      client.upload(
        `${url}/upload`,
        {
          filename: 'short.txt',
          sizeBytes: 4,
          open: () => Readable.from(['abc']),
        },
        attempt(),
      ),
    ).rejects.toMatchObject({ code: 'artifact_size_mismatch' })
  })

  it('rejects untrusted upload URLs before opening the artifact', async () => {
    const client = new SlackClient(credentials.botToken)
    for (const url of [
      'https://slack.com.evil.test/upload',
      'http://files.slack.com/upload',
      'https://user:password@files.slack.com/upload',
      'https://files.slack.com:444/upload',
    ]) {
      await expect(
        client.upload(
          url,
          {
            filename: 'a',
            sizeBytes: 1,
            open: () => {
              throw new Error('must not open')
            },
          },
          attempt(),
        ),
      ).rejects.toMatchObject({ code: 'invalid_upload_url' })
    }
  })

  it('destroys an upload source on cancellation', async () => {
    const started = deferred()
    const url = await slackServer((request, response) => {
      request.on('data', () => {
        started.resolve()
      })
      request.on('end', () => response.end('OK'))
    })
    let sent = false
    const source = new Readable({
      read() {
        if (!sent) {
          sent = true
          this.push(Buffer.alloc(64 * 1024))
        }
      },
    })
    const controller = new AbortController()
    const pending = new SlackClient(credentials.botToken, url).upload(
      `${url}/upload`,
      {
        filename: 'large',
        sizeBytes: 1024 * 1024,
        open: () => source,
      },
      attempt(controller.signal),
    )
    const rejected = expect(pending).rejects.toMatchObject({ code: 'transport_failed' })
    await started.promise
    const closed = once(source, 'close')
    controller.abort()
    await rejected
    await closed
    expect(source.destroyed).toBe(true)
  })
})
