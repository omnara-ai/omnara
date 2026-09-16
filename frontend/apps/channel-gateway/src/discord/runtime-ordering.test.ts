import { describe, expect, it, vi } from 'vitest'

import { testRuntimeHandle } from '../gateway-test-fixtures'
import { RuntimeLoop, type RuntimeLoopOptions } from '../runtime/loop'
import { createDiscordFactory } from './factory'
import { discordCapability } from './gateway'
import { localIdentifyRedis, redisAvailable } from './identify-test-support'
import { DiscordAPIError } from './protocol'
import { runDiscordUnit } from './runtime'
import { ready, runtimeSetup, session } from './runtime-test-support'
import type { DiscordSocketOptions } from './socket'
import { socketProvider } from './socket-provider-test-support'
import { config, deferred } from './test-support'

describe.skipIf(!redisAvailable)('Discord quiet live-owner reconstruction', () => {
  const redis = localIdentifyRedis()
  it('republishes lost startup bits through the installed worker public heartbeat without new dispatches', async () => {
    const provider = await socketProvider()
    const f = await runtimeSetup({ configuration: { shard_id: 0, shard_count: 4 } })
    const options = {
      ...f.options,
      apiUrl: provider.api.href,
      redis: redis(),
      createSocket: undefined,
      stopTimeoutMs: 2000,
    }
    const running = runDiscordUnit(f.factory, f.unit, f.context, options)
    const key = `omnara:discord:identify:{${config.applicationID}}:started:4`
    try {
      await vi.waitFor(
        async () => {
          expect(await redis().getBit(key, 0)).toBe(1)
        },
        { timeout: 3000 },
      )
      expect(f.context.updateCheckpoint).toHaveBeenCalledOnce()
      expect(f.context.updateCheckpoint.mock.calls[0]?.[0]).toMatchObject({
        version: 1,
        checkpoint: {
          shard_id: 0,
          shard_count: 4,
          initial_ready_seen: true,
          session: { sessionId: 'packaged-worker-session', sequence: 1 },
        },
      })
      await redis().del(key)
      expect(await redis().getBit(key, 0)).toBe(0)
      await vi.waitFor(
        async () => {
          expect(await redis().getBit(key, 0)).toBe(1)
        },
        { timeout: 3000 },
      )
      expect(provider.identified).toHaveLength(1)
      expect(f.context.updateCheckpoint).toHaveBeenCalledOnce()
      expect(f.context.submitInbound).not.toHaveBeenCalled()
    } finally {
      f.controller.abort()
      await running
    }
    expect(provider.failures).toEqual([])
    expect(f.options.onFatalRuntimeFailure).not.toHaveBeenCalled()
  }, 10_000)
})

describe.skipIf(!redisAvailable)('Discord startup deferral through existing supervision', () => {
  const redis = localIdentifyRedis()
  it('releases high shards filling both slots so lower shards can claim; fewer slots cannot sustain all shards', async () => {
    const f = await runtimeSetup()
    const bothHigh = deferred()
    const entered: number[] = []
    const started: number[] = []
    const stopped: number[] = []
    const createSocket = (hooks: DiscordSocketOptions) => ({
      connect: async () => {
        const id = hooks.shard.shard_id
        entered.push(id)
        if (entered.length === 2) bothHigh.resolve()
        if (id >= 2) await bothHigh.promise
        // The same public Identify-hook failure path as the real SDK wrapper.
        try {
          await hooks.identify.waitForIdentify(id, hooks.signal)
        } catch (cause) {
          const error =
            cause instanceof DiscordAPIError ? cause : new DiscordAPIError('identify_failed')
          hooks.onFailure(error)
          throw error
        }
        started.push(id)
        hooks.updateSessionInfo(id, { ...session, shardId: id, shardCount: 4 })
        hooks.onDispatch(ready(), id)
      },
      stop: () => {
        stopped.push(hooks.shard.shard_id)
        return Promise.resolve()
      },
    })
    const metadata = {
      url: 'wss://gateway.discord.gg',
      shards: 4,
      session_start_limit: { total: 100, remaining: 20, reset_after: 60_000, max_concurrency: 2 },
    }
    // Seed only current provider metadata. No startup facts, permits or receipts.
    const prefix = `omnara:discord:identify:{${config.applicationID}}`
    await redis().set(
      `${prefix}:state`,
      JSON.stringify({
        info: metadata,
        remaining: 20,
        resetAt: Date.now() + 60_000,
        cachedUntil: Date.now() + 60_000,
      }),
    )
    const runtime = await createDiscordFactory({
      ...f.options,
      redis: redis(),
      createSocket,
      processReceipt: vi.fn(),
    }).create(f.factory)
    const runUnit = runtime.runUnit
    if (!runUnit) throw new Error('expected Discord runtime')
    const pending = [2, 3, 0, 1].map((id) => ({
      ...f.unit,
      id: `unit-${id}`,
      unit_key: `discord_gateway:${id}`,
      configuration: { shard_id: id, shard_count: 4 },
    }))
    const client = {
      claimRuntimeUnits: vi.fn<RuntimeLoopOptions['client']['claimRuntimeUnits']>(() => {
        const next = pending.shift()
        return Promise.resolve(next ? [next] : [])
      }),
      heartbeatRuntimeUnit: vi.fn<RuntimeLoopOptions['client']['heartbeatRuntimeUnit']>((unit) =>
        Promise.resolve(unit),
      ),
      releaseRuntimeUnit: vi
        .fn<RuntimeLoopOptions['client']['releaseRuntimeUnit']>()
        .mockResolvedValue(undefined),
    }
    const registry = {
      acquire: vi.fn<RuntimeLoopOptions['registry']['acquire']>(() =>
        Promise.resolve(
          testRuntimeHandle({
            configuration: f.factory.configuration,
            runtime,
            runUnit: (unit, context) => runUnit(unit, { ...f.context, ...context }),
          }),
        ),
      ),
    }
    const loop = new RuntimeLoop({
      capabilities: [discordCapability],
      claimLimit: 2,
      client,
      registry,
      idlePollMs: 1,
      leaseMs: 30_000,
      logger: f.factory.logger,
      owner: 'ordering-test',
      reserveWorkBytes: f.budget.reserve,
      stopTimeoutMs: 100,
    })
    const running = loop.run(f.controller.signal)
    try {
      await vi.waitFor(() => {
        expect(started).toEqual(expect.arrayContaining([0, 1]))
      })
      expect(new Set(entered.slice(0, 2))).toEqual(new Set([2, 3]))
      expect(stopped).toEqual(expect.arrayContaining([2, 3]))
      expect(client.releaseRuntimeUnit.mock.calls.map(([unit]) => unit.id)).toEqual(
        expect.arrayContaining(['unit-2', 'unit-3']),
      )
      for (const [, error] of client.releaseRuntimeUnit.mock.calls)
        expect(error.message).toContain('identify_startup_deferred')
      expect(await redis().get(`${prefix}:state`)).toContain('"remaining":18')
      expect(started).toHaveLength(2)
      // Both slots now hold live lower shards. More steady-state capacity is
      // necessary to host the other two; no rotation or fake success is claimed.
      expect(client.claimRuntimeUnits).toHaveBeenCalledTimes(4)
    } finally {
      bothHigh.resolve()
      f.controller.abort()
      await running
      await runtime.close()
    }
    expect(f.budget.usedBytes).toBe(0)
    expect(client.releaseRuntimeUnit).toHaveBeenCalledTimes(4)
  })
})
