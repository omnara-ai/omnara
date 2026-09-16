import type { ChannelSendOperation, JsonBody } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import type { GatewayOperation } from '../operations/envelope'
import { createDiscordGateway, discordCapability, type DiscordGatewayOptions } from './gateway'
import {
  app,
  destination,
  identity,
  installation,
  json,
  message,
  room,
  server,
  suffix,
  thread,
} from './test-support'

const scope = {
  project_id: installation.install.project_id,
  integration_app_id: app.app.id,
  integration_install_id: installation.install.id,
  agent_id: `agt_${suffix}`,
  channel_id: `itgt_${suffix}`,
}
const input = { destination, message: { text: 'Hello' }, params: {} } satisfies ChannelSendOperation
function setup(apiUrl?: string) {
  const core = {
    getAppConfiguration: vi
      .fn<DiscordGatewayOptions['core']['getAppConfiguration']>()
      .mockResolvedValue(app),
    getInstallationConfiguration: vi
      .fn<DiscordGatewayOptions['core']['getInstallationConfiguration']>()
      .mockResolvedValue(installation),
    publishDefinition: vi
      .fn<DiscordGatewayOptions['core']['publishDefinition']>()
      .mockImplementation((_scope, body) => Promise.resolve({ ...body, id: `cdef_${suffix}` })),
  }
  return { core, ...createDiscordGateway({ core, apiUrl }) }
}
function request(payload: JsonBody = input): GatewayOperation {
  return {
    kind: 'send',
    scope,
    capability: discordCapability,
    requestId: 'discord-operation',
    deadlineMs: Date.now() + 5000,
    payloadJSON: JSON.stringify(payload),
    artifacts: [],
  }
}
const signal = () => new AbortController().signal

describe('Discord operation composition', () => {
  it.each([true, false])(
    'preserves known root publication when thread success=%s',
    async (success) => {
      let posts = 0
      const apiUrl = await server((req, res) => {
        if (identity(req, res)) return
        if (req.method === 'GET') json(res, room)
        else if (req.url === `/channels/${room.id}/messages`) {
          posts++
          json(res, message)
        } else json(res, success ? thread : { code: 50013 }, success ? 200 : 403)
      })
      const gateway = setup(apiUrl)
      const result = await gateway.executeOperation(
        request({ ...input, reply_channel_grants: { receive: true, read: false, send: true } }),
        [],
        signal(),
      )
      expect(result).toMatchObject({
        outcome: 'completed',
        payload: {
          publication: 'published',
          message_channel: 'destination',
          message_id: message.id,
        },
      })
      if (success)
        expect(result.payload).toMatchObject({
          reply_channel: { provider_ref: thread.id, implementation_key: 'discord_thread' },
        })
      else
        expect(result.payload).toMatchObject({
          continuation_error: { code: 'reply_channel_unavailable' },
        })
      expect(result.payload).not.toHaveProperty(success ? 'continuation_error' : 'reply_channel')
      expect(posts).toBe(1)
      expect(gateway.core.publishDefinition).toHaveBeenCalledExactlyOnceWith(
        scope,
        expect.objectContaining({ implementation_key: 'discord_thread', kind: 'DISCORD_THREAD' }),
        expect.any(AbortSignal),
      )
    },
  )

  it('does not contact Discord when the required child definition cannot be published', async () => {
    const provider = vi.fn<Parameters<typeof server>[0]>((_req, res) => {
      json(res, room)
    })
    const gateway = setup(await server(provider))
    gateway.core.publishDefinition.mockRejectedValue(new Error('core unavailable'))
    expect(
      await gateway.executeOperation(
        request({ ...input, reply_channel_grants: { receive: true, read: false, send: true } }),
        [],
        signal(),
      ),
    ).toEqual({ outcome: 'failed' })
    expect(provider).not.toHaveBeenCalled()
  })

  it.each(['project', 'app', 'install', 'revision', 'provider', 'connector'])(
    'rejects %s scope mismatch before provider I/O',
    async (field) => {
      const gateway = setup('http://127.0.0.1:1/')
      if (field === 'app')
        gateway.core.getAppConfiguration.mockResolvedValue({
          ...app,
          app: { ...app.app, id: 'iapp_wrong' },
        })
      else if (field === 'provider' || field === 'connector')
        gateway.core.getAppConfiguration.mockResolvedValue({
          ...app,
          app: {
            ...app.app,
            provider: field === 'provider' ? 'slack' : 'discord',
            connector_key: field === 'connector' ? 'other' : 'omnara',
          },
        })
      else
        gateway.core.getInstallationConfiguration.mockResolvedValue({
          ...installation,
          app_configuration_revision: field === 'revision' ? 2 : 1,
          install: {
            ...installation.install,
            project_id: field === 'project' ? 'proj_wrong' : scope.project_id,
            id: field === 'install' ? 'iin_wrong' : scope.integration_install_id,
          },
        })
      expect(await gateway.executeOperation(request(), [], signal())).toEqual({ outcome: 'failed' })
    },
  )

  it('rejects interactions, wrong implementation and duplicate fields before config lookup', async () => {
    const gateway = setup()
    for (const operation of [
      { ...request(), kind: 'interaction' as const, scope },
      request({ ...input, destination: { ...destination, implementation_key: 'slack_channel' } }),
      { ...request(), payloadJSON: '{"message":{},"mess\\u0061ge":{}}' },
    ])
      expect(await gateway.executeOperation(operation, [], signal())).toEqual({ outcome: 'failed' })
    expect(gateway.core.getAppConfiguration).not.toHaveBeenCalled()
  })

  it('reads preserved text and publishes both real thread and parent definitions on resolve', async () => {
    const apiUrl = await server((req, res) => {
      if (identity(req, res)) return
      if (req.url?.includes('/messages')) json(res, [message])
      else json(res, req.url === `/channels/${thread.id}` ? thread : room)
    })
    const gateway = setup(apiUrl)
    const read = { ...request({ destination, limit: 10 }), kind: 'read' as const, scope }
    expect(await gateway.executeOperation(read, [], signal())).toMatchObject({
      outcome: 'completed',
      payload: { messages: [{ content: { text: message.content } }] },
    })
    const resolve = {
      ...request({ provider_ref: thread.id }),
      kind: 'resolve_address' as const,
      scope: {
        project_id: scope.project_id,
        integration_app_id: scope.integration_app_id,
        integration_install_id: scope.integration_install_id,
      },
    }
    expect(await gateway.executeOperation(resolve, [], signal())).toMatchObject({
      outcome: 'completed',
      payload: { provider_ref: thread.id, parent: { provider_ref: room.id } },
    })
    expect(gateway.core.publishDefinition.mock.calls.map((call) => call[1].kind)).toEqual([
      'DISCORD_THREAD',
      'DISCORD_CHANNEL',
    ])
  })

  it('returns only a fixed resolver failure code and keeps infrastructure failure unclassified', async () => {
    const gateway = setup()
    const operation = {
      ...request({ provider_ref: 'invalid' }),
      kind: 'resolve_address' as const,
      scope: {
        project_id: scope.project_id,
        integration_app_id: scope.integration_app_id,
        integration_install_id: scope.integration_install_id,
      },
    }
    expect(await gateway.executeOperation(operation, [], signal())).toEqual({
      outcome: 'failed',
      payload: { code: 'invalid_address' },
    })
    gateway.core.getAppConfiguration.mockRejectedValue(new Error('private core detail'))
    expect(await gateway.executeOperation(operation, [], signal())).toEqual({ outcome: 'failed' })
  })
})
