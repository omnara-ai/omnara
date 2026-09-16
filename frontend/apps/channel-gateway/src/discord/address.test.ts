import { ApiError } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'
import { z } from 'zod'

import { discordDefinition, resolveDiscordAddress } from './address'
import { DiscordClient } from './client'
import {
  config,
  identity,
  json,
  operation,
  registration,
  room,
  server,
  thread,
} from './test-support'

describe('Discord address resolution', () => {
  it.each([room, thread])(
    'resolves verified same-guild address $id without writes',
    async (target) => {
      const paths: string[] = []
      const url = await server((request, response) => {
        paths.push(`${request.method} ${request.url}`)
        if (identity(request, response)) return
        json(response, request.url === `/channels/${room.id}` ? room : thread)
      })
      const context = registration()
      const result = await resolveDiscordAddress(
        new DiscordClient(config, url),
        { provider_ref: target.id },
        context,
        operation(),
      )
      expect(result.provider_ref).toBe(target.id)
      expect(result.display_name).toBe(target.name)
      expect(result.provider_ref_kind).toBe(target.type === 0 ? 'channel' : 'thread')
      expect(result.parent?.provider_ref).toBe(target.type === 11 ? room.id : undefined)
      expect(context.publishDefinition).toHaveBeenCalledTimes(target.type === 0 ? 1 : 2)
      expect(paths.every((path) => path.startsWith('GET '))).toBe(true)
    },
  )
  it('publishes named kinds, file support and dashboard interaction fallback honestly', () => {
    expect(discordDefinition('channel')).toMatchObject({
      kind: 'DISCORD_CHANNEL',
      capabilities: {
        creates_reply_channel: true,
        artifacts: true,
        permissions: false,
        questions: false,
      },
    })
    expect(discordDefinition('thread')).toMatchObject({
      kind: 'DISCORD_THREAD',
      capabilities: { creates_reply_channel: false },
    })
  })
  it.each([
    { input: { provider_ref: 'https://discord.com/channels/1/2' }, code: 'invalid_address' },
    { input: { provider_ref: room.id, provider_ref_kind: 'dm' }, code: 'unsupported_address' },
  ])('rejects invalid input before network I/O', async ({ input, code }) => {
    let calls = 0
    const url = await server((_request, response) => {
      calls++
      json(response, {})
    })
    await expect(
      resolveDiscordAddress(new DiscordClient(config, url), input, registration(), operation()),
    ).rejects.toMatchObject({ code })
    expect(calls).toBe(0)
  })
  it.each([
    { channel: { ...room, guild_id: '666666666666666666' }, code: 'address_unavailable' },
    { channel: { ...room, id: '666666666666666666' }, code: 'address_unavailable' },
    { channel: { ...room, type: 15 }, code: 'unsupported_address' },
    { channel: { id: room.id, type: 1 }, code: 'unsupported_address' },
    { channel: { id: room.id, type: 0 }, code: 'permanent_failure' },
  ])('rejects unsupported or mismatched native facts', async ({ channel, code }) => {
    const url = await server((request, response) => {
      if (!identity(request, response)) json(response, z.json().parse(channel))
    })
    const context = registration()
    await expect(
      resolveDiscordAddress(
        new DiscordClient(config, url),
        { provider_ref: room.id },
        context,
        operation(),
      ),
    ).rejects.toMatchObject({ code })
    expect(context.publishDefinition).not.toHaveBeenCalled()
  })
  it.each([
    { ...room, guild_id: '666666666666666666' },
    { ...room, id: '666666666666666666' },
    { ...room, type: 15 },
  ])('verifies the actual parent instead of trusting thread metadata', async (parent) => {
    const url = await server((request, response) => {
      if (!identity(request, response))
        json(response, request.url === `/channels/${thread.id}` ? thread : parent)
    })
    const context = registration()
    await expect(
      resolveDiscordAddress(
        new DiscordClient(config, url),
        { provider_ref: thread.id },
        context,
        operation(),
      ),
    ).rejects.toThrow()
    expect(context.publishDefinition).not.toHaveBeenCalled()
  })
  it.each(['resource', 'identity', 'publish'])(
    'keeps only resource 404s in the address diagnosis: %s',
    async (failure) => {
      const url = await server((request, response) => {
        if (failure === 'identity' && request.url === '/applications/@me') {
          json(response, {}, 404)
          return
        }
        if (identity(request, response)) return
        json(response, room, failure === 'resource' ? 404 : 200)
      })
      const context = registration()
      if (failure === 'publish')
        context.publishDefinition.mockRejectedValue(new ApiError(404, 'private'))
      await expect(
        resolveDiscordAddress(
          new DiscordClient(config, url),
          { provider_ref: room.id },
          context,
          operation(),
        ),
      ).rejects.toMatchObject({
        code:
          failure === 'resource'
            ? 'address_unavailable'
            : failure === 'publish'
              ? 'outcome_unknown'
              : 'permanent_failure',
      })
    },
  )
})
