import { Readable } from 'node:stream'

import { describe, expect, it } from 'vitest'

import { SlackClient } from './client'
import { sendSlackMessage } from './send'
import {
  body,
  credentials,
  deferred,
  json,
  operation,
  slackPayload,
  slackServer,
} from './test-support'

describe('sendSlackMessage', () => {
  it.each([
    '*hello* @user :smile:',
    '```js\nconst mentions = "@here @channel @user :thinking:"\n```\n<@U123> <#C123> <https://example.com|link>',
  ])('preserves native text and exact thread timestamps in one post (%s)', async (text) => {
    let payload: unknown
    const url = await slackServer((request, response) => {
      void body(request).then((bytes) => {
        payload = slackPayload(bytes)
        json(response, { ok: true, channel: 'C1', ts: '1720000000.000002' })
      })
    })
    const result = await sendSlackMessage(
      new SlackClient(credentials.botToken, url),
      {
        channel: 'C1',
        threadTs: '1720000000.000001',
        text,
      },
      operation(),
    )
    expect(payload).toEqual({
      channel: 'C1',
      thread_ts: '1720000000.000001',
      text,
    })
    expect(result).toEqual({
      channel: 'C1',
      threadTs: '1720000000.000001',
      publication: 'published',
      messageTs: '1720000000.000002',
      fileIds: [],
    })
  })

  it('stages all files and publishes once with initial_comment, without another text post', async () => {
    const calls: { path?: string; payload: unknown; authorization?: string }[] = []
    let nextFile = 0
    const url = await slackServer((request, response) => {
      void body(request).then((bytes) => {
        const upload = request.url?.startsWith('/upload/')
        const payload: unknown = upload ? bytes.toString() : slackPayload(bytes)
        calls.push({ path: request.url, payload, authorization: request.headers.authorization })
        if (request.url === '/files.getUploadURLExternal') {
          nextFile++
          json(response, {
            ok: true,
            file_id: `F${nextFile}`,
            upload_url: `${url}/upload/${nextFile}`,
          })
        } else if (upload) response.end('OK')
        else if (request.url === '/files.completeUploadExternal')
          json(response, { ok: true, files: [{ id: 'F1' }, { id: 'F2' }] })
        else json(response, { ok: false, error: 'unexpected_call' }, 400)
      })
    })
    const result = await sendSlackMessage(
      new SlackClient(credentials.botToken, url),
      {
        channel: 'C1',
        threadTs: '100.000001',
        text: 'Both files together',
        artifacts: [
          { filename: 'a.txt', sizeBytes: 3, open: () => Readable.from(['abc']) },
          { filename: 'b.txt', sizeBytes: 4, open: () => Readable.from(['defg']) },
        ],
      },
      operation(),
    )
    expect(calls.map((call) => call.path)).toEqual([
      '/files.getUploadURLExternal',
      '/upload/1',
      '/files.getUploadURLExternal',
      '/upload/2',
      '/files.completeUploadExternal',
    ])
    expect(calls.at(-1)?.payload).toEqual({
      files: [
        { id: 'F1', title: 'a.txt' },
        { id: 'F2', title: 'b.txt' },
      ],
      channel_id: 'C1',
      thread_ts: '100.000001',
      initial_comment: 'Both files together',
    })
    expect(
      calls.filter((call) => call.path?.startsWith('/upload/')).map((call) => call.authorization),
    ).toEqual([undefined, undefined])
    expect(result).toMatchObject({ publication: 'published', fileIds: ['F1', 'F2'] })
    expect(result.messageTs).toBeUndefined()
  })

  it('uses a real file-share timestamp when Slack supplies one', async () => {
    const url = await slackServer((request, response) => {
      if (request.url === '/files.getUploadURLExternal')
        json(response, { ok: true, file_id: 'F1', upload_url: `${url}/upload` })
      else if (request.url === '/upload') {
        request.resume()
        response.end('OK')
      } else
        json(response, {
          ok: true,
          files: [{ id: 'F1', shares: { public: { C1: [{ ts: '100.000001' }] } } }],
        })
    })
    const result = await sendSlackMessage(
      new SlackClient(credentials.botToken, url),
      {
        channel: 'C1',
        artifacts: [{ filename: 'a', sizeBytes: 1, open: () => Readable.from(['a']) }],
      },
      operation(),
    )
    expect(result.messageTs).toBe('100.000001')
  })

  it('does not claim one publication timestamp from incomplete or contradictory file shares', async () => {
    let tickets = 0
    const url = await slackServer((request, response) => {
      if (request.url === '/files.getUploadURLExternal') {
        tickets++
        json(response, { ok: true, file_id: `F${tickets}`, upload_url: `${url}/upload` })
      } else if (request.url === '/upload') {
        request.resume()
        response.end('OK')
      } else
        json(response, {
          ok: true,
          files: [
            { id: 'F1', shares: { public: { C1: [{ ts: '100.000001' }] } } },
            { id: 'F2', shares: { public: { C2: [{ ts: '100.000001' }] } } },
          ],
        })
    })
    const result = await sendSlackMessage(
      new SlackClient(credentials.botToken, url),
      {
        channel: 'C1',
        artifacts: ['a', 'b'].map((name) => ({
          filename: name,
          sizeBytes: 1,
          open: () => Readable.from([name]),
        })),
      },
      operation(),
    )
    expect(result.publication).toBe('published')
    expect(result.messageTs).toBeUndefined()
  })

  it('retries a rate-limited completion using the same staged files', async () => {
    let uploads = 0
    let tickets = 0
    let completions = 0
    const url = await slackServer((request, response) => {
      if (request.url === '/files.getUploadURLExternal') {
        tickets++
        json(response, { ok: true, file_id: 'F1', upload_url: `${url}/upload` })
      } else if (request.url === '/upload') {
        uploads++
        request.resume()
        response.end('OK')
      } else if (++completions === 1) json(response, {}, 429, { 'retry-after': '0' })
      else json(response, { ok: true })
    })
    await sendSlackMessage(
      new SlackClient(credentials.botToken, url),
      {
        channel: 'C1',
        text: 'hi',
        artifacts: [{ filename: 'a', sizeBytes: 1, open: () => Readable.from(['a']) }],
      },
      operation(),
    )
    expect({ tickets, uploads, completions }).toEqual({ tickets: 1, uploads: 1, completions: 2 })
  })

  it('retries failed staging without publishing an abandoned ticket', async () => {
    let tickets = 0
    let uploads = 0
    let completion: unknown
    const url = await slackServer((request, response) => {
      if (request.url === '/files.getUploadURLExternal') {
        tickets++
        json(response, { ok: true, file_id: `F${tickets}`, upload_url: `${url}/upload` })
      } else if (request.url === '/upload') {
        request.resume()
        if (++uploads === 1) json(response, {}, 503)
        else response.end('OK')
      } else {
        void body(request).then((bytes) => {
          completion = slackPayload(bytes)
          json(response, { ok: true })
        })
      }
    })
    await sendSlackMessage(
      new SlackClient(credentials.botToken, url),
      {
        channel: 'C1',
        artifacts: [{ filename: 'a', sizeBytes: 1, open: () => Readable.from(['a']) }],
      },
      operation(),
    )
    expect({ tickets, uploads }).toEqual({ tickets: 2, uploads: 2 })
    expect(completion).toMatchObject({ files: [{ id: 'F2', title: 'a' }] })
  })

  it('stops immediately on missing scope without retrying or claiming unknown delivery', async () => {
    let calls = 0
    const url = await slackServer((_request, response) => {
      calls++
      json(response, { ok: false, error: 'missing_scope' })
    })
    await expect(
      sendSlackMessage(
        new SlackClient(credentials.botToken, url),
        {
          channel: 'C1',
          text: 'hello',
        },
        operation(),
      ),
    ).rejects.toMatchObject({ code: 'permanent_failure', outcomeUnknown: false, attempts: 1 })
    expect(calls).toBe(1)
  })

  it('makes at most four known-safe attempts and does not shorten Retry-After', async () => {
    let calls = 0
    const url = await slackServer((_request, response) => {
      calls++
      json(response, {}, 429, { 'retry-after': '0' })
    })
    await expect(
      sendSlackMessage(
        new SlackClient(credentials.botToken, url),
        { channel: 'C1', text: 'hi' },
        operation(),
      ),
    ).rejects.toMatchObject({ code: 'retries_exhausted', attempts: 4, outcomeUnknown: false })
    expect(calls).toBe(4)
    let longCalls = 0
    const slow = await slackServer((_request, response) => {
      longCalls++
      json(response, {}, 429, { 'retry-after': '60' })
    })
    await expect(
      sendSlackMessage(
        new SlackClient(credentials.botToken, slow),
        { channel: 'C1', text: 'hi' },
        operation(2_000),
      ),
    ).rejects.toMatchObject({ code: 'deadline_exceeded', attempts: 1, outcomeUnknown: false })
    expect(longCalls).toBe(1)
  })

  it.each(['internal_error', 'http500', 'lost_response'])(
    'does not resend after ambiguous publication: %s',
    async (failure) => {
      let publications = 0
      const url = await slackServer((request, response) => {
        publications++
        if (failure === 'lost_response') request.socket.destroy()
        else if (failure === 'http500') json(response, {}, 500)
        else json(response, { ok: false, error: 'internal_error' })
      })
      await expect(
        sendSlackMessage(
          new SlackClient(credentials.botToken, url),
          { channel: 'C1', text: 'hi' },
          operation(),
        ),
      ).rejects.toMatchObject({ code: 'outcome_unknown', attempts: 1, outcomeUnknown: true })
      expect(publications).toBe(1)
    },
  )

  it('does not repeat file staging or completion after an ambiguous file publication', async () => {
    const calls: string[] = []
    const url = await slackServer((request, response) => {
      calls.push(request.url ?? '')
      if (request.url === '/files.getUploadURLExternal')
        json(response, { ok: true, file_id: 'F1', upload_url: `${url}/upload` })
      else if (request.url === '/upload') {
        request.resume()
        response.end('OK')
      } else json(response, { ok: false, error: 'fatal_error' })
    })
    await expect(
      sendSlackMessage(
        new SlackClient(credentials.botToken, url),
        {
          channel: 'C1',
          artifacts: [{ filename: 'a', sizeBytes: 1, open: () => Readable.from(['a']) }],
        },
        operation(),
      ),
    ).rejects.toMatchObject({ outcomeUnknown: true, attempts: 1 })
    expect(calls).toEqual([
      '/files.getUploadURLExternal',
      '/upload',
      '/files.completeUploadExternal',
    ])
  })

  it('aborts a stalled SDK publication without retrying an unknown send', async () => {
    const started = deferred()
    const closed = deferred()
    let calls = 0
    const url = await slackServer((_request, response) => {
      calls++
      response.writeHead(200, { 'content-type': 'application/json' })
      response.write('{"ok":true,')
      response.once('close', () => {
        closed.resolve()
      })
      started.resolve()
    })
    const controller = new AbortController()
    const pending = sendSlackMessage(
      new SlackClient(credentials.botToken, url),
      { channel: 'C1', text: 'hi' },
      { ...operation(), signal: controller.signal },
    )
    const rejected = expect(pending).rejects.toMatchObject({
      code: 'canceled',
      attempts: 1,
      outcomeUnknown: true,
    })
    await started.promise
    controller.abort()
    await rejected
    await closed.promise
    expect(calls).toBe(1)
  })

  it('reports cancellation during staging as not published', async () => {
    const started = deferred()
    const url = await slackServer(() => {
      started.resolve()
    })
    const controller = new AbortController()
    const pending = sendSlackMessage(
      new SlackClient(credentials.botToken, url),
      {
        channel: 'C1',
        artifacts: [{ filename: 'a', sizeBytes: 1, open: () => Readable.from(['a']) }],
      },
      { ...operation(), signal: controller.signal },
    )
    const rejected = expect(pending).rejects.toMatchObject({
      code: 'canceled',
      outcomeUnknown: false,
    })
    await started.promise
    controller.abort()
    await rejected
  })

  it('rejects invalid destinations, empty messages, too many files and text Slack would truncate', async () => {
    const client = new SlackClient(credentials.botToken)
    for (const input of [
      { channel: 'C1', text: ' ' },
      { channel: '../C1', text: 'hi' },
      { channel: 'C1', text: 'x'.repeat(40_001) },
      {
        channel: 'C1',
        artifacts: Array.from({ length: 21 }, () => ({
          filename: 'a',
          sizeBytes: 1,
          open: () => Readable.from(['a']),
        })),
      },
    ]) {
      await expect(sendSlackMessage(client, input, operation())).rejects.toMatchObject({
        outcomeUnknown: false,
      })
    }
  })
})
