import { describe, expect, it, vi } from 'vitest'

import { abortableDelay } from '../async'
import { DiscordIdentify, type DiscordIdentifyOptions } from './identify'
import { commitIdentifyState } from './identify-scripts'
import { localIdentifyRedis, redisAvailable } from './identify-test-support'
import { config, deferred } from './test-support'

const info = {
  url: 'wss://gateway.discord.gg',
  shards: 4,
  session_start_limit: { total: 1000, remaining: 10, reset_after: 60_000, max_concurrency: 2 },
}

// Optional local integration gate: owns a Unix-socket Redis child, never an
// existing server/database. No ports, FLUSH, provider credentials or network.
describe.skipIf(!redisAvailable)('Discord Identify Lua against local Redis', () => {
  const redis = localIdentifyRedis()
  let counter = 0n
  function setup(overrides: Partial<DiscordIdentifyOptions> = {}) {
    const applicationID = String(BigInt(config.applicationID) + ++counter)
    const fetchGatewayInfo = vi.fn().mockResolvedValue(info)
    const options = {
      redis: redis(),
      shard: { shard_id: 0, shard_count: 4 },
      initialReadySeen: () => false,
      applicationID,
      fetchGatewayInfo,
      signal: new AbortController().signal,
      ...overrides,
    }
    return {
      gate: new DiscordIdentify(options),
      options,
      fetchGatewayInfo,
      forShard: (id: number, initialized = false) =>
        new DiscordIdentify({
          ...options,
          shard: { shard_id: id, shard_count: options.shard.shard_count },
          initialReadySeen: () => initialized,
        }),
      prefix: `omnara:discord:identify:{${options.applicationID}}`,
    }
  }

  it('coalesces metadata across independent coordinators and atomically reserves shared budget', async () => {
    const f = setup()
    const other = f.forShard(1)
    await Promise.all([
      f.gate.waitForIdentify(0, f.options.signal),
      other.waitForIdentify(1, f.options.signal),
    ])
    expect(f.fetchGatewayInfo).toHaveBeenCalledOnce()
    expect(await redis().get(`${f.prefix}:state`)).toContain('"remaining":8')
    expect(await redis().pTTL(`${f.prefix}:state`)).toBe(-1)
    expect(await redis().pTTL(`${f.prefix}:bucket:0`)).toBeGreaterThan(0)
    expect(await redis().pTTL(`${f.prefix}:bucket:1`)).toBeGreaterThan(0)
  })

  it('spaces reused buckets during independent initialized-shard recovery', async () => {
    const f = setup()
    const granted = new Map<number, number>()
    const identify = async (shard: number) => {
      await f.forShard(shard, true).waitForIdentify(shard, f.options.signal)
      granted.set(shard, Date.now())
    }
    await identify(2)
    await identify(3)
    // Historical READY permits independent recovery, but never skips the budget.
    expect([...granted.keys()]).toEqual([2, 3])
    await Promise.all([identify(1), identify(0)])
    for (const [lower, higher] of [
      [0, 2],
      [1, 3],
    ] as const) {
      const first = granted.get(higher)
      const second = granted.get(lower)
      if (first === undefined || second === undefined) throw new Error('missing Identify permit')
      expect(second - first).toBeGreaterThanOrEqual(5000)
    }
  }, 10_000)

  it('never refunds an aborted waiter or grants while Redis is unavailable', async () => {
    const f = setup()
    await f.gate.waitForIdentify(0, f.options.signal)
    const controller = new AbortController()
    const waiting = f.gate.waitForIdentify(0, controller.signal)
    const rejected = expect(waiting).rejects.toThrow()
    controller.abort()
    await rejected
    expect(await redis().get(`${f.prefix}:state`)).toContain('"remaining":9')
    const broken = setup({
      redis: {
        set: vi.fn().mockRejectedValue(new Error('offline')),
        eval: vi.fn().mockRejectedValue(new Error('offline')),
      },
    })
    await expect(broken.gate.waitForIdentify(0, broken.options.signal)).rejects.toThrow('offline')
  })

  it('caps global starts even when different buckets remain free; total is never a refill', async () => {
    const f = setup()
    f.fetchGatewayInfo.mockResolvedValue({
      ...info,
      session_start_limit: { ...info.session_start_limit, remaining: 1 },
    })
    await f.gate.waitForIdentify(0, f.options.signal)
    await expect(f.forShard(1).waitForIdentify(1, f.options.signal)).rejects.toThrow(
      'identify_budget_exhausted',
    )
    expect((await f.gate.gatewayInfo(f.options.signal)).session_start_limit.remaining).toBe(1)
  })

  it('requires fresh provider truth after reset and preserves zero-reset reservations', async () => {
    const f = setup({ metadataCacheMs: 10 })
    f.fetchGatewayInfo.mockResolvedValue({
      ...info,
      session_start_limit: { ...info.session_start_limit, remaining: 1, reset_after: 0 },
    })
    await f.gate.waitForIdentify(0, f.options.signal)
    await abortableDelay(20)
    await expect(f.forShard(1).waitForIdentify(1, f.options.signal)).rejects.toThrow(
      'identify_budget_exhausted',
    )
    expect(f.fetchGatewayInfo.mock.calls.length).toBeGreaterThanOrEqual(2)
    const fresh = setup({ metadataCacheMs: 10 })
    fresh.fetchGatewayInfo.mockResolvedValue({
      ...info,
      session_start_limit: { ...info.session_start_limit, remaining: 1, reset_after: 20 },
    })
    await fresh.gate.waitForIdentify(0, fresh.options.signal)
    await abortableDelay(30)
    await fresh.forShard(1).waitForIdentify(1, fresh.options.signal)
    expect(fresh.fetchGatewayInfo.mock.calls.length).toBeGreaterThanOrEqual(2)
  })

  it('fences an expired refresh owner and never changes a successor lock', async () => {
    const f = setup()
    await redis().set(`${f.prefix}:refresh`, 'successor', {
      expiration: { type: 'PX', value: 1000 },
    })
    expect(
      await redis().eval(commitIdentifyState, {
        keys: [`${f.prefix}:state`, `${f.prefix}:refresh`],
        arguments: ['expired', JSON.stringify(info), '1000'],
      }),
    ).toBe(0)
    expect(await redis().get(`${f.prefix}:refresh`)).toBe('successor')
    expect(await redis().get(`${f.prefix}:state`)).toBeNull()
  })

  it('aborts metadata refresh and does not turn its late response into a permit', async () => {
    const f = setup()
    const blocked = deferred()
    f.fetchGatewayInfo.mockImplementation(async () => {
      await blocked.promise
      return info
    })
    const controller = new AbortController()
    const waiting = f.gate.waitForIdentify(0, controller.signal)
    const rejected = expect(waiting).rejects.toThrow()
    await vi.waitFor(() => {
      expect(f.fetchGatewayInfo).toHaveBeenCalledOnce()
    })
    controller.abort()
    await rejected
    blocked.resolve()
    expect(await redis().get(`${f.prefix}:state`)).toBeNull()
  })
})
