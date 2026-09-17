import type { SessionInfo } from '@discordjs/ws'
import type { ChannelConnectorRuntimeUnit } from '@omnara/sdk'
import { onTestFinished, vi } from 'vitest'

import { testRuntimeUnit } from '../gateway-test-fixtures'
import type { ProviderFactoryContext, RuntimeUnitContext } from '../types'
import { WorkByteBudget } from '../work-budget'
import type { DiscordRuntimeOptions } from './runtime'
import type { DiscordDispatch, DiscordSocketOptions } from './socket'
import { app, config, installation, json, message, server, user } from './test-support'

export const session: SessionInfo = {
  sessionId: 'test-session',
  sequence: 1,
  shardId: 0,
  shardCount: 1,
  resumeURL: 'wss://gateway.discord.gg',
}

export function savedCheckpoint(value: SessionInfo | null = session, initialReady = true) {
  return {
    shard_id: value?.shardId ?? 0,
    shard_count: value?.shardCount ?? 1,
    initial_ready_seen: initialReady,
    session: value,
  }
}

export function dispatch(sequence: number): DiscordDispatch {
  // SAFETY: This native MESSAGE_CREATE fixture supplies the SDK-required message
  // fields; only its string enum discriminant differs from the exported TS enum.
  return {
    op: 0,
    t: 'MESSAGE_CREATE',
    s: sequence,
    d: {
      ...message,
      id: String(BigInt(message.id) + BigInt(sequence)),
      author: { ...user, avatar: null, discriminator: '0', global_name: null },
      type: 0,
      mentions: [],
      mention_roles: [],
      mention_everyone: false,
      edited_timestamp: null,
      pinned: false,
      tts: false,
    },
  } as DiscordDispatch // Native JSON fixture crosses the SDK's string-enum boundary.
}

export function ready(
  sequence = 1,
  botID = config.botUserID,
  sessionID = session.sessionId,
): DiscordDispatch {
  // SAFETY: The fixture uses the complete native READY body. Its string enum
  // discriminant crosses the same SDK boundary as MESSAGE_CREATE above.
  return {
    op: 0,
    t: 'READY',
    s: sequence,
    d: {
      v: 10,
      user: { ...user, id: botID, avatar: null, discriminator: '0', global_name: null },
      guilds: [],
      session_id: sessionID,
      resume_gateway_url: session.resumeURL,
      application: { id: config.applicationID, flags: 1 << 19, flags_new: '0' },
      shard: [0, 1],
    },
  } as DiscordDispatch
}

export async function runtimeSetup(saved: Partial<ChannelConnectorRuntimeUnit> = {}) {
  const apiUrl = await server((request, response) => {
    if (request.url === '/applications/@me')
      json(response, { id: config.applicationID, flags: 1 << 19 })
    else if (request.url === '/users/@me') json(response, user)
    else json(response, {}, 404)
  })
  const unit = testRuntimeUnit({
    integration_app_id: app.app.id,
    runtime_kind: 'discord_gateway',
    unit_key: 'discord_gateway:0',
    configuration: { shard_id: 0, shard_count: 1 },
    ...saved,
  })
  const controller = new AbortController()
  const budget = new WorkByteBudget(32 * 1024 * 1024)
  const factory = {
    configuration: app,
    signal: controller.signal,
    resolveInstallation: vi
      .fn<ProviderFactoryContext['resolveInstallation']>()
      .mockResolvedValue(installation),
    reserveWorkBytes: budget.reserve,
    logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
  } satisfies ProviderFactoryContext
  const context = {
    signal: controller.signal,
    reserveWorkBytes: budget.reserve,
    updateCheckpoint: vi.fn<RuntimeUnitContext['updateCheckpoint']>(),
    resolveInteraction: vi.fn<RuntimeUnitContext['resolveInteraction']>(),
    submitInbound: vi
      .fn<RuntimeUnitContext['submitInbound']>()
      .mockResolvedValue({ receipt_id: 'receipt-1', state: 'pending' }),
  } satisfies RuntimeUnitContext
  let hooks: DiscordSocketOptions | undefined
  const connect = vi.fn(() => Promise.resolve())
  const stop = vi.fn(() => {
    hooks?.updateSessionInfo(0, null)
    return Promise.resolve()
  })
  const createSocket = vi.fn((value: DiscordSocketOptions) => {
    hooks = value
    return { connect, stop }
  })
  const options: DiscordRuntimeOptions = {
    apiUrl,
    stopTimeoutMs: 100,
    createSocket,
    onFatalRuntimeFailure: vi.fn(),
    redis: { eval: vi.fn().mockResolvedValue(0), set: vi.fn().mockResolvedValue('OK') },
  }
  onTestFinished(() => {
    controller.abort()
  })
  return {
    unit,
    controller,
    budget,
    factory,
    context,
    options,
    connect,
    stop,
    createSocket,
    socket: () => {
      if (!hooks) throw new Error('socket not started')
      return hooks
    },
  }
}
