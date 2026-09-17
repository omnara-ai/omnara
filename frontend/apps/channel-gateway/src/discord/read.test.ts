import { describe, expect, it } from 'vitest'

import { DiscordClient } from './client'
import { readDiscordOperation } from './read'
import {
  config,
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

describe('Discord history', () => {
  it('returns newest-first pages with exact scoped snowflake cursors', async () => {
    const newest = { ...message, id: '900719925474099399' }
    const older = { ...message, id: '900719925474099398' }
    const oldest = { ...message, id: '900719925474099397' }
    const paths: string[] = []
    const url = await server((request, response) => {
      paths.push(request.url ?? '')
      if (identity(request, response)) return
      if (request.url === `/channels/${room.id}`) json(response, room)
      else json(response, request.url?.includes('before=') ? [oldest] : [newest, older])
    })
    const client = new DiscordClient(config, url)
    const first = await readDiscordOperation(client, { destination, limit: 2 }, operation())
    expect(first.coverage).toBe('complete')
    expect(first.messages.map((item) => item.message_id)).toEqual([newest.id, older.id])
    expect(first.messages[0]?.content.text).toBe(message.content)
    expect(first.messages[0]?.author?.ref).toBe(config.botUserID)
    const second = await readDiscordOperation(
      client,
      { destination, limit: 2, cursor: first.next_cursor },
      operation(),
    )
    expect(second.messages[0]?.message_id).toBe(oldest.id)
    expect(second.next_cursor).toBeUndefined()
    expect(paths).toContain(`/channels/${room.id}/messages?limit=2&before=${older.id}`)
  })
  it('reports files/rich content and ambiguous empty history as partial without invented artifacts', async () => {
    let page = 0
    const url = await server((request, response) => {
      if (identity(request, response)) return
      if (request.url === `/channels/${room.id}`) {
        json(response, room)
        return
      }
      json(
        response,
        page++ === 0
          ? [{ ...message, attachments: [{ id: room.id }], embeds: [{ description: 'embed' }] }]
          : [],
      )
    })
    const client = new DiscordClient(config, url)
    const first = await readDiscordOperation(client, { destination, limit: 10 }, operation())
    expect(first.coverage).toBe('partial')
    expect(first.messages[0]?.content.artifact_ids).toBeUndefined()
    expect(first.messages[0]?.metadata).toEqual({ discord_attachment_ids: [room.id] })
    expect(first.next_cursor).toBeUndefined()
    const empty = await readDiscordOperation(client, { destination, limit: 10 }, operation())
    expect(empty.coverage).toBe('partial')
    expect(empty.coverage_reason).toContain('permission')
  })
  it('keeps existing child facts and thread-root reply references without registration', async () => {
    const url = await server((request, response) => {
      if (identity(request, response)) return
      if (request.url === `/channels/${room.id}`) {
        json(response, room)
        return
      }
      if (request.url === `/channels/${thread.id}`) {
        json(response, thread)
        return
      }
      json(
        response,
        request.url?.includes(thread.id)
          ? [
              {
                ...message,
                id: '666666666666666666',
                channel_id: thread.id,
                content: '',
                type: 21,
                message_reference: {
                  message_id: thread.id,
                  channel_id: room.id,
                  guild_id: config.guildID,
                },
                referenced_message: {
                  ...message,
                  content: 'parent content, not this message',
                  author: { id: '123456789012345678', username: 'Parent author' },
                  timestamp: '2020-01-01T00:00:00Z',
                },
              },
            ]
          : [{ ...message, thread }],
      )
    })
    const client = new DiscordClient(config, url)
    const root = await readDiscordOperation(client, { destination, limit: 10 }, operation())
    expect(root.messages[0]?.reply_channel).toEqual({
      implementation_key: 'discord_thread',
      provider_ref: thread.id,
      provider_ref_kind: 'thread',
      display_name: thread.name,
    })
    const child = await readDiscordOperation(
      client,
      { destination: threadDestination, limit: 10 },
      operation(),
    )
    expect(child.messages[0]?.reply_to).toEqual({
      message_id: thread.id,
      destination: {
        implementation_key: 'discord_channel',
        provider_ref: room.id,
        provider_ref_kind: 'channel',
      },
    })
    expect(child.messages[0]?.message_id).toBe('666666666666666666')
    expect(child.messages[0]?.content).toEqual({})
    expect(child.messages[0]?.author).toEqual({ ref: config.botUserID, display_name: 'Omnara' })
    expect(child.messages[0]?.created_at).toBe('2026-09-15T12:00:00.000Z')
    expect(child.coverage).toBe('partial')
  })

  it('retains raw code/mention text and real reply IDs instead of SDK plain text', async () => {
    const content = '```ts\nconst text = ":smile: <@123456789012345678>"\n```\n**bold** @Ada'
    const replyID = '123456789012345678'
    const url = await server((request, response) => {
      if (identity(request, response)) return
      if (request.url === `/channels/${room.id}`) json(response, room)
      else
        json(response, [
          {
            ...message,
            content,
            type: 19,
            message_reference: { message_id: replyID, channel_id: room.id },
            author: { ...message.author, global_name: '' },
          },
        ])
    })
    const result = await readDiscordOperation(
      new DiscordClient(config, url),
      { destination, limit: 10 },
      operation(),
    )
    expect(result.messages[0]).toMatchObject({
      message_id: message.id,
      content: { text: content },
      reply_to: { message_id: replyID },
      author: { ref: config.botUserID, display_name: '' },
    })
  })

  it.each(
    [null, {}, [message, { ...message, id: '444444444444444443' }]].map((page) => ({ page })),
  )('rejects malformed pages and pages over the requested limit: $page', async ({ page }) => {
    const url = await server((request, response) => {
      if (identity(request, response)) return
      json(response, request.url === `/channels/${room.id}` ? room : page)
    })
    await expect(
      readDiscordOperation(new DiscordClient(config, url), { destination, limit: 1 }, operation()),
    ).rejects.toMatchObject({ code: 'permanent_failure', outcomeUnknown: false, attempts: 1 })
  })
  it.each(
    [
      [{ ...message, channel_id: '666666666666666666' }],
      [{ ...message, guild_id: '666666666666666666' }],
      [message, message],
      [{ ...message, id: '100000000000000001' }, message],
    ].map((messages) => ({ messages })),
  )('rejects cross-scope, duplicate or nonprogressing pages', async ({ messages }) => {
    const url = await server((request, response) => {
      if (identity(request, response)) return
      json(response, request.url === `/channels/${room.id}` ? room : messages)
    })
    await expect(
      readDiscordOperation(new DiscordClient(config, url), { destination, limit: 10 }, operation()),
    ).rejects.toThrow()
  })
  it.each([
    'not a cursor',
    Buffer.from(
      JSON.stringify({ guild: config.guildID, channel: thread.id, before: message.id }),
    ).toString('base64url'),
    Buffer.from(
      JSON.stringify({ guild: thread.id, channel: room.id, before: message.id }),
    ).toString('base64url'),
    Buffer.from(
      `{"guild":"${config.guildID}","channel":"${room.id}","before":"${message.id}","before":"${message.id}"}`,
    ).toString('base64url'),
  ])('rejects foreign or malformed cursors before I/O', async (cursor) => {
    let calls = 0
    const url = await server((_request, response) => {
      calls++
      json(response, {})
    })
    await expect(
      readDiscordOperation(
        new DiscordClient(config, url),
        { destination, limit: 10, cursor },
        operation(),
      ),
    ).rejects.toMatchObject({ code: 'invalid_history_cursor' })
    expect(calls).toBe(0)
  })
})
