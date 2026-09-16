import type { WebSocketManager } from '@discordjs/ws'
import { type IShardingStrategy, WebSocketShardEvents } from '@discordjs/ws'
import { describe, expect, it, onTestFinished, vi } from 'vitest'

import { discordAPIURL, discordIntents } from './bootstrap'
import { DiscordAPIError } from './protocol'
import { dispatch, session } from './runtime-test-support'
import { createDiscordSocket, type DiscordSocketOptions } from './socket'
import { config, deferred } from './test-support'

const gatewayInfo = {
  url: 'wss://gateway.discord.gg',
  shards: 4,
  session_start_limit: { total: 100, remaining: 50, reset_after: 60_000, max_concurrency: 2 },
}
function setup(overrides: Partial<DiscordSocketOptions> = {}) {
  let manager: WebSocketManager | undefined
  const controller = new AbortController()
  const strategy = {
    spawn: vi.fn<IShardingStrategy['spawn']>().mockResolvedValue(undefined),
    connect: vi.fn<IShardingStrategy['connect']>().mockResolvedValue(undefined),
    destroy: vi.fn<IShardingStrategy['destroy']>().mockResolvedValue(undefined),
    send: vi.fn<IShardingStrategy['send']>().mockResolvedValue(undefined),
    fetchStatus: (): never => {
      throw new Error('unexpected status call')
    },
  }
  const identify = {
    gatewayInfo: vi
      .fn<DiscordSocketOptions['identify']['gatewayInfo']>()
      .mockResolvedValue(gatewayInfo),
    waitForIdentify: vi
      .fn<DiscordSocketOptions['identify']['waitForIdentify']>()
      .mockResolvedValue(undefined),
  }
  const options: DiscordSocketOptions = {
    botToken: config.botToken,
    api: discordAPIURL(),
    shard: { shard_id: 2, shard_count: 4 },
    signal: controller.signal,
    stopTimeoutMs: 50,
    identify,
    retrieveSessionInfo: vi.fn().mockReturnValue(session),
    updateSessionInfo: vi.fn(),
    onDispatch: vi.fn(),
    onHeartbeat: vi.fn(),
    onFailure: vi.fn(),
    onFatalRuntimeFailure: vi.fn(),
    buildStrategy: (value) => {
      manager = value
      return strategy
    },
    ...overrides,
  }
  const socket = createDiscordSocket(options)
  onTestFinished(async () => {
    controller.abort()
    await socket.stop().catch(() => undefined)
  })
  return {
    socket,
    options,
    strategy,
    controller,
    identify,
    manager: () => {
      if (!manager) throw new Error('manager not created')
      return manager
    },
  }
}

describe('Discord public SDK composition', () => {
  it('pins global shard count, uses the shared public metadata hook, and preserves raw dispatch', async () => {
    const f = setup()
    await f.socket.connect()
    expect(f.strategy.spawn).toHaveBeenCalledWith([2])
    expect(f.manager().options.shardCount).toBe(4)
    expect(f.manager().options.intents).toBe(discordIntents)
    expect(f.identify.gatewayInfo).toHaveBeenCalledOnce()
    expect(f.manager().options.retrieveSessionInfo(0)).toEqual(session)
    f.manager().emit(WebSocketShardEvents.Dispatch, dispatch(2), 2)
    expect(f.options.onDispatch).toHaveBeenCalledWith(dispatch(2), 2)
    expect(f.options.onFailure).not.toHaveBeenCalled()
  })

  it('stops explicitly when public Identify coordination rejects', async () => {
    const f = setup()
    vi.mocked(f.identify.waitForIdentify).mockRejectedValue(new Error('Redis unavailable'))
    const throttler = await f.manager().options.buildIdentifyThrottler(f.manager())
    await expect(throttler.waitForIdentify(2, f.controller.signal)).rejects.toThrow(
      'identify_failed',
    )
    expect(f.options.onFailure).toHaveBeenCalledOnce()
  })

  it('preserves startup deferral for ordinary unit release and forwards only live heartbeat evidence', async () => {
    const f = setup()
    const deferred = new DiscordAPIError('identify_startup_deferred')
    f.identify.waitForIdentify.mockRejectedValue(deferred)
    const throttler = await f.manager().options.buildIdentifyThrottler(f.manager())
    await expect(throttler.waitForIdentify(2, f.controller.signal)).rejects.toBe(deferred)
    expect(f.options.onFailure).toHaveBeenCalledWith(deferred)
    const stats = { ackAt: 2, heartbeatAt: 1, latency: 1 }
    f.manager().emit(WebSocketShardEvents.HeartbeatComplete, stats, 2)
    expect(f.options.onHeartbeat).toHaveBeenCalledExactlyOnceWith(2)
    await f.socket.stop()
    f.manager().emit(WebSocketShardEvents.HeartbeatComplete, stats, 2)
    expect(f.options.onHeartbeat).toHaveBeenCalledOnce()
  })

  it('keeps actual zero-remaining SDK rejection rather than forging a resume exception', async () => {
    const f = setup()
    vi.mocked(f.identify.gatewayInfo).mockResolvedValue({
      ...gatewayInfo,
      session_start_limit: { ...gatewayInfo.session_start_limit, remaining: 0 },
    })
    await expect(f.socket.connect()).rejects.toThrow('Not enough sessions remaining')
    expect(f.strategy.connect).not.toHaveBeenCalled()
  })

  it('destroys a worker that finishes spawning after cancellation, without starting its socket', async () => {
    const f = setup()
    const blocked = deferred()
    f.strategy.spawn.mockReturnValue(blocked.promise)
    const connecting = f.socket.connect()
    const rejected = expect(connecting).rejects.toThrow()
    await vi.waitFor(() => {
      expect(f.strategy.spawn).toHaveBeenCalledOnce()
    })
    f.strategy.destroy.mockClear() // SDK initially destroys its empty shard set.
    const stopping = f.socket.stop()
    expect(f.strategy.destroy).not.toHaveBeenCalled()
    blocked.resolve()
    await stopping
    await rejected
    expect(f.strategy.destroy).toHaveBeenCalledOnce()
    expect(f.strategy.connect).not.toHaveBeenCalled()
  })

  it('marks health unrecoverable when public worker shutdown never acknowledges', async () => {
    const f = setup({ stopTimeoutMs: 10 })
    f.strategy.destroy.mockReturnValue(new Promise(() => undefined))
    await expect(f.socket.stop()).rejects.toThrow('gateway_stop_failed')
    expect(f.options.onFatalRuntimeFailure).toHaveBeenCalledOnce()
    expect(f.socket.stop()).toBe(f.socket.stop())
  })

  it('reports fatal provider configuration closes and ignores callbacks after stop', async () => {
    const f = setup()
    f.manager().emit(WebSocketShardEvents.Closed, 4014, 2)
    expect(f.options.onFailure).toHaveBeenCalledOnce()
    await f.socket.stop()
    f.manager().emit(WebSocketShardEvents.Dispatch, dispatch(2), 2)
    expect(f.options.onDispatch).not.toHaveBeenCalled()
  })
})
