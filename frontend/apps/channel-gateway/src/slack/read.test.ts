import { describe, expect, it } from 'vitest'
import { z } from 'zod'

import { SlackClient } from './client'
import { parseSlackMessage } from './messages'
import { slackMessage } from './protocol'
import { readSlackHistory } from './read'
import {
  body,
  credentials,
  deferred,
  json,
  operation,
  slackPayload,
  slackServer,
} from './test-support'

const historyRequest = z.object({
  channel: z.string(),
  limit: z.number(),
  inclusive: z.boolean(),
  latest: z.string().optional(),
  cursor: z.string().optional(),
})

describe('readSlackHistory', () => {
  it('reports provider-limited history as partial even without another cursor', async () => {
    const url = await slackServer((_request, response) => {
      json(response, {
        ok: true,
        is_limited: true,
        messages: [{ ts: '100.000001', text: 'visible' }],
      })
    })
    const page = await readSlackHistory(
      new SlackClient(credentials.botToken, url),
      parseSlackMessage,
      { channel: 'C1', limit: 1 },
      operation(),
    )
    expect(page.coverage).toBe('partial')
    expect(page.nextCursor).toBeUndefined()
  })

  it('uses channel history for channels and returns chronological pages toward older messages', async () => {
    const requests: unknown[] = []
    const url = await slackServer((request, response) => {
      void body(request).then((bytes) => {
        const payload = historyRequest.parse(slackPayload(bytes))
        requests.push({ path: request.url, ...payload })
        json(
          response,
          payload.latest
            ? { ok: true, messages: [{ ts: '100.000001', text: 'oldest', user: 'U1' }] }
            : {
                ok: true,
                has_more: true,
                messages: [
                  { ts: '100.000003', text: '*newest*', user: 'U1' },
                  { ts: '100.000002', text: 'middle' },
                ],
              },
        )
      })
    })
    const client = new SlackClient(credentials.botToken, url)
    const parser = parseSlackMessage
    const first = await readSlackHistory(client, parser, { channel: 'C1', limit: 2 }, operation())
    expect(first.messages.map((message) => message.timestamp)).toEqual(['100.000002', '100.000003'])
    expect(first.messages[1]?.text.trim()).toBe('*newest*')
    expect(first.coverage).toBe('complete')
    const last = await readSlackHistory(
      client,
      parser,
      { channel: 'C1', limit: 2, cursor: first.nextCursor },
      operation(),
    )
    expect(last.messages.map((message) => message.timestamp)).toEqual(['100.000001'])
    expect(last.nextCursor).toBeUndefined()
    expect(requests).toEqual([
      { path: '/conversations.history', channel: 'C1', limit: 2, inclusive: false },
      {
        path: '/conversations.history',
        channel: 'C1',
        limit: 2,
        inclusive: false,
        latest: '100.000002',
      },
    ])
  })

  it('finds the actual newest replies beyond the first provider page, preserving timestamp precision', async () => {
    const all = Array.from({ length: 205 }, (_, index) => ({
      ts: `9007199254740992.${String(index + 1).padStart(6, '0')}`,
      text: `reply ${index}`,
      user: 'U1',
    }))
    let calls = 0
    const url = await slackServer((request, response) => {
      void body(request).then((bytes) => {
        calls++
        const payload = historyRequest.parse(slackPayload(bytes))
        const before = payload.latest
        const eligible = all.filter((message) => !before || message.ts < before)
        const offset = Number(payload.cursor ?? '0')
        const page = eligible.slice(offset, offset + 100)
        json(response, {
          ok: true,
          messages: page,
          response_metadata: {
            next_cursor: offset + 100 < eligible.length ? String(offset + 100) : '',
          },
        })
      })
    })
    const client = new SlackClient(credentials.botToken, url)
    const parser = parseSlackMessage
    const first = await readSlackHistory(
      client,
      parser,
      { channel: 'C1', threadTs: '9007199254740992.000001', limit: 2 },
      operation(),
    )
    expect(first.messages.map((message) => message.timestamp)).toEqual(
      all.slice(-2).map((message) => message.ts),
    )
    expect(calls).toBe(3)
    const older = await readSlackHistory(
      client,
      parser,
      { channel: 'C1', threadTs: '9007199254740992.000001', limit: 2, cursor: first.nextCursor },
      operation(),
    )
    expect(older.messages.map((message) => message.timestamp)).toEqual(
      all.slice(-4, -2).map((message) => message.ts),
    )
  })

  it('rejects a cursor from another destination before any provider request', async () => {
    let calls = 0
    const url = await slackServer((_request, response) => {
      calls++
      json(response, { ok: true, has_more: true, messages: [{ ts: '100.000001', text: 'one' }] })
    })
    const client = new SlackClient(credentials.botToken, url)
    const parser = parseSlackMessage
    const first = await readSlackHistory(client, parser, { channel: 'C1', limit: 1 }, operation())
    await expect(
      readSlackHistory(
        client,
        parser,
        { channel: 'C2', limit: 1, cursor: first.nextCursor },
        operation(),
      ),
    ).rejects.toMatchObject({ code: 'invalid_history_cursor' })
    expect(calls).toBe(1)
  })

  it('does not return an incomplete scan as the newest thread page', async () => {
    const started = deferred()
    let calls = 0
    const url = await slackServer((_request, response) => {
      if (++calls === 1)
        json(response, {
          ok: true,
          messages: [{ ts: '100.000001', text: 'old root' }],
          response_metadata: { next_cursor: 'next' },
        })
      else started.resolve()
    })
    const controller = new AbortController()
    const pending = readSlackHistory(
      new SlackClient(credentials.botToken, url),
      parseSlackMessage,
      {
        channel: 'C1',
        threadTs: '100.000001',
        limit: 1,
      },
      { ...operation(), signal: controller.signal },
    )
    const rejected = expect(pending).rejects.toMatchObject({ code: 'canceled' })
    await started.promise
    controller.abort()
    await rejected
  })

  it('rejects nonadvancing provider pagination', async () => {
    let calls = 0
    const url = await slackServer((_request, response) => {
      calls++
      json(response, {
        ok: true,
        messages: [{ ts: '100.000001', text: 'root' }],
        response_metadata: { next_cursor: 'same' },
      })
    })
    await expect(
      readSlackHistory(
        new SlackClient(credentials.botToken, url),
        parseSlackMessage,
        {
          channel: 'C1',
          threadTs: '100.000001',
          limit: 1,
        },
        operation(),
      ),
    ).rejects.toMatchObject({ code: 'permanent_failure', attempts: 1 })
    expect(calls).toBe(2)
  })

  it('reports incomplete file/rich-content representation and never exposes SDK download callbacks', () => {
    const parse = parseSlackMessage
    const result = parse(
      slackMessage.parse({
        ts: '100.000001',
        text: 'see file',
        user: 'U1',
        files: [{ id: 'F1', name: 'report.pdf', url_private: 'https://private.example/secret' }],
        blocks: [{ type: 'image' }],
      }),
    )
    expect(result).toEqual({
      timestamp: '100.000001',
      text: 'see file',
      authorRef: 'U1',
      files: [{ id: 'F1', name: 'report.pdf' }],
      partial: true,
    })
    expect(JSON.stringify(result)).not.toContain('private.example')
    expect(parse({ ts: '100.000002', text: '<https://example.com|link>' }).text).toContain(
      '<https://example.com|link>',
    )
  })
})
