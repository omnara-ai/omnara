import { Readable } from 'node:stream'

import { type ChannelSendOperation, schemas } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import { SlackClient } from './client'
import { createSlackMessageParser } from './messages'
import { readSlackOperation, sendSlackOperation, slackDestination } from './operations'
import { body, credentials, json, operation, slackServer } from './test-support'

const input: ChannelSendOperation = {
  destination: {
    implementation_key: 'slack_channel',
    provider_ref: 'C1',
    provider_ref_kind: 'channel',
    provider_metadata: {},
  },
  message: { text: 'hello' },
  params: {},
  reply_channel_grants: { receive: true, read: false, send: true },
}

describe('Slack generated operation mapping', () => {
  it('keeps a root post in destination and supplies only permitted child facts', async () => {
    const url = await slackServer((_request, response) => {
      json(response, { ok: true, channel: 'C1', ts: '100.000001' })
    })
    const result = await sendSlackOperation(
      new SlackClient(credentials.botToken, url),
      input,
      [],
      'slack_thread',
      operation(),
    )
    expect(schemas.zChannelSendOperationResult.parse(result)).toEqual({
      publication: 'published',
      message_channel: 'destination',
      message_id: '100.000001',
      reply_channel: {
        implementation_key: 'slack_thread',
        provider_ref: 'C1:100.000001',
        provider_ref_kind: 'thread',
      },
    })
    expect(JSON.stringify(result)).not.toContain('grants')
    expect(input.reply_channel_grants).toEqual({ receive: true, read: false, send: true })
  })

  it('returns no child without captured grants or for an existing thread', async () => {
    const url = await slackServer((_request, response) => {
      json(response, { ok: true, channel: 'C1', ts: '100.000002' })
    })
    const client = new SlackClient(credentials.botToken, url)
    const root = await sendSlackOperation(
      client,
      {
        ...input,
        reply_channel_grants: undefined,
      },
      [],
      'slack_thread',
      operation(),
    )
    const thread = await sendSlackOperation(
      client,
      {
        ...input,
        destination: {
          ...input.destination,
          implementation_key: 'slack_thread',
          provider_ref: 'C1:100.000001',
          provider_ref_kind: 'thread',
        },
      },
      [],
      'slack_thread',
      operation(),
    )
    expect(root.reply_channel).toBeUndefined()
    expect(thread.reply_channel).toBeUndefined()
    expect(thread.message_channel).toBe('destination')
  })

  it('uses accepted artifact order and never invents a message or continuation ID', async () => {
    const requests: { path?: string; data: unknown }[] = []
    let tickets = 0
    const url = await slackServer((request, response) => {
      void body(request).then((bytes) => {
        requests.push({
          path: request.url,
          data: request.url === '/upload' ? bytes.toString() : JSON.parse(bytes.toString()),
        })
        if (request.url === '/files.getUploadURLExternal') {
          tickets++
          json(response, { ok: true, file_id: `F${tickets}`, upload_url: `${url}/upload` })
        } else if (request.url === '/upload') response.end('OK')
        else json(response, { ok: true, files: [{ id: 'F1' }, { id: 'F2' }] })
      })
    })
    const result = await sendSlackOperation(
      new SlackClient(credentials.botToken, url),
      {
        ...input,
        message: { text: 'files', artifact_ids: ['a', 'b'] },
      },
      [
        { id: 'b', filename: 'b.txt', sizeBytes: 1, open: () => Readable.from(['b']) },
        { id: 'a', filename: 'a.txt', sizeBytes: 1, open: () => Readable.from(['a']) },
      ],
      'slack_thread',
      operation(),
    )
    expect(
      requests.filter((request) => request.path === '/upload').map((request) => request.data),
    ).toEqual(['a', 'b'])
    expect(result).toMatchObject({
      publication: 'published',
      message_channel: 'destination',
      metadata: { slack_file_ids: ['F1', 'F2'] },
    })
    expect(result.message_id).toBeUndefined()
    expect(result.reply_channel).toBeUndefined()
  })

  it('rejects unsupported params, mismatched files and malformed addresses before I/O', async () => {
    let calls = 0
    const url = await slackServer((_request, response) => {
      calls++
      json(response, { ok: true, ts: '100.000001' })
    })
    const client = new SlackClient(credentials.botToken, url)
    await expect(
      sendSlackOperation(
        client,
        { ...input, params: { channel: 'C2' } },
        [],
        'slack_thread',
        operation(),
      ),
    ).rejects.toMatchObject({ code: 'unsupported_params' })
    await expect(
      sendSlackOperation(
        client,
        { ...input, message: { artifact_ids: ['missing'] } },
        [],
        'slack_thread',
        operation(),
      ),
    ).rejects.toMatchObject({ code: 'artifact_mismatch' })
    await expect(sendSlackOperation(client, input, [], '', operation())).rejects.toMatchObject({
      code: 'invalid_configuration',
    })
    expect(() =>
      slackDestination({
        ...input.destination,
        provider_ref_kind: 'thread',
        provider_ref: 'C1:100.000001:extra',
      }),
    ).toThrow('invalid_destination')
    expect(calls).toBe(0)
  })

  it('maps generated read input to native history without inventing artifact IDs', async () => {
    const url = await slackServer((_request, response) => {
      json(response, {
        ok: true,
        messages: [{ ts: '100.000001', text: 'file', files: [{ id: 'F1' }] }],
      })
    })
    const page = await readSlackOperation(
      new SlackClient(credentials.botToken, url),
      createSlackMessageParser(credentials),
      { destination: input.destination, limit: 1 },
      operation(),
    )
    expect(page.coverage).toBe('partial')
    expect(schemas.zChannelReadOperationResult.parse(page)).toMatchObject({
      messages: [
        {
          content: { text: 'file\n' },
          publication: 'published',
          message_id: '100.000001',
          metadata: { slack_file_ids: ['F1'] },
        },
      ],
      coverage: 'partial',
    })
    expect(JSON.stringify(page)).not.toContain('artifact_id')
  })

  it('bounds observed Unicode text and represents unavailable content explicitly', async () => {
    const url = await slackServer((_request, response) => {
      json(response, {
        ok: true,
        messages: [
          { ts: '100.000002', text: '😀'.repeat(20_000) },
          { ts: '100.000001', text: '' },
        ],
      })
    })
    const page = await readSlackOperation(
      new SlackClient(credentials.botToken, url),
      createSlackMessageParser(credentials),
      { destination: input.destination, limit: 2 },
      operation(),
    )
    expect(schemas.zChannelReadOperationResult.safeParse(page).success).toBe(true)
    expect(page.messages[0]?.content).toEqual({})
    const text = page.messages[1]?.content.text ?? ''
    expect(Buffer.byteLength(text)).toBeLessThanOrEqual(64 * 1024)
    expect(text).not.toContain('\ufffd')
    expect(page.coverage).toBe('partial')
    expect(page.coverage_reason).toContain('bounded')
  })
})
