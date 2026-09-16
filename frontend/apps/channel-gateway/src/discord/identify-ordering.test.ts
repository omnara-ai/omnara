import { describe, expect, it, vi } from 'vitest'

import { discordAPIURL } from './bootstrap'
import { DiscordCheckpoint } from './checkpoint'
import { DiscordIdentify, type DiscordIdentifyOptions } from './identify'
import { localIdentifyRedis, redisAvailable } from './identify-test-support'
import { savedCheckpoint, session } from './runtime-test-support'
import { config, deferred } from './test-support'

describe.skipIf(!redisAvailable)('Discord initial Identify ordering against Redis', () => {
  const redis = localIdentifyRedis()
  let counter = 1000n
  function setup() {
    const applicationID = String(BigInt(config.applicationID) + ++counter)
    const signal = new AbortController().signal
    const fetchGatewayInfo = vi.fn().mockResolvedValue({
      url: 'wss://gateway.discord.gg',
      shards: 4,
      session_start_limit: { total: 1000, remaining: 20, reset_after: 60_000, max_concurrency: 2 },
    })
    const gate = (id: number, overrides: Partial<DiscordIdentifyOptions> = {}) =>
      new DiscordIdentify({
        redis: redis(),
        applicationID,
        signal,
        fetchGatewayInfo,
        shard: { shard_id: id, shard_count: 4 },
        initialReadySeen: () => false,
        ...overrides,
      })
    const prefix = `omnara:discord:identify:{${applicationID}}`
    return { gate, prefix, started: `${prefix}:started:4`, signal, fetchGatewayInfo }
  }

  it('defers higher groups atomically without taking bucket or global budget', async () => {
    const f = setup()
    for (const id of [3, 2])
      await expect(f.gate(id).waitForIdentify(id, f.signal)).rejects.toThrow(
        'identify_startup_deferred',
      )
    expect(await redis().get(`${f.prefix}:state`)).toContain('"remaining":20')
    expect(await redis().exists([`${f.prefix}:bucket:0`, `${f.prefix}:bucket:1`])).toBe(0)
    await f.gate(0).waitForIdentify(0, f.signal)
    await f.gate(1).waitForIdentify(1, f.signal)
    // Permits and SDK-observed sessions do not establish READY. Simulate death
    // after both Identify permits but before either identity-validated READY.
    expect(await redis().exists(f.started)).toBe(0)
    await expect(f.gate(2).waitForIdentify(2, f.signal)).rejects.toThrow(
      'identify_startup_deferred',
    )
    expect(await redis().get(`${f.prefix}:state`)).toContain('"remaining":18')
    await f.gate(0, { initialReadySeen: () => true }).publishReady(f.signal)
    await expect(f.gate(2).waitForIdentify(2, f.signal)).rejects.toThrow(
      'identify_startup_deferred',
    )
  })

  it('allows the next initial group only after READY while retaining the full bucket interval', async () => {
    const f = setup()
    await Promise.all([0, 1].map((id) => f.gate(id).waitForIdentify(id, f.signal)))
    const firstStart = Date.now()
    await Promise.all(
      [0, 1].map((id) => f.gate(id, { initialReadySeen: () => true }).publishReady(f.signal)),
    )
    await Promise.all([2, 3].map((id) => f.gate(id).waitForIdentify(id, f.signal)))
    expect(Date.now() - firstStart).toBeGreaterThanOrEqual(5000)
    expect(await redis().get(`${f.prefix}:state`)).toContain('"remaining":16')
  }, 10_000)

  it('recovers an initialized high shard independently after session invalidation and Redis loss', async () => {
    const f = setup()
    const saved = { ...savedCheckpoint(null), shard_id: 3, shard_count: 4 }
    const checkpoint = new DiscordCheckpoint(
      { shard_id: 3, shard_count: 4 },
      {
        checkpoint_version: 1,
        checkpoint: saved,
      },
      discordAPIURL(),
      vi.fn(),
    )
    const gate = f.gate(3, { initialReadySeen: () => checkpoint.initialReadySeen })
    expect(checkpoint.retrieve(3)).toBeNull()
    await gate.publishReady(f.signal)
    expect(await redis().getBit(f.started, 3)).toBe(1)
    expect(await redis().getBit(f.started, 0)).toBe(0)
    await gate.waitForIdentify(3, f.signal)
    expect(await redis().get(`${f.prefix}:state`)).toContain('"remaining":19')
    expect(await redis().pTTL(`${f.prefix}:bucket:1`)).toBeGreaterThan(0)
    // A still-initial shard cannot treat this independent recovery as lower evidence.
    await expect(f.gate(2).waitForIdentify(2, f.signal)).rejects.toThrow(
      'identify_startup_deferred',
    )
  })

  it('cannot use initialized recovery to bypass exhausted global allowance', async () => {
    const f = setup()
    f.fetchGatewayInfo.mockResolvedValue({
      url: 'wss://gateway.discord.gg',
      shards: 4,
      session_start_limit: { total: 1000, remaining: 1, reset_after: 60_000, max_concurrency: 2 },
    })
    await f.gate(3, { initialReadySeen: () => true }).waitForIdentify(3, f.signal)
    await expect(
      f.gate(2, { initialReadySeen: () => true }).waitForIdentify(2, f.signal),
    ).rejects.toThrow('identify_budget_exhausted')
    expect(await redis().get(`${f.prefix}:state`)).toContain('"remaining":0')
  })

  it('honors its own confirmed Redis fact when the owner died before checkpoint renewal', async () => {
    const f = setup()
    await f.gate(3, { initialReadySeen: () => true }).publishReady(f.signal)
    expect(await redis().getBit(f.started, 0)).toBe(0)
    // Fresh process with an older empty checkpoint; the existing own bit still
    // proves READY for this exact app/shard/count and invents no lower facts.
    await f.gate(3).waitForIdentify(3, f.signal)
    expect(await redis().get(`${f.prefix}:state`)).toContain('"remaining":19')
    await expect(f.gate(2).waitForIdentify(2, f.signal)).rejects.toThrow(
      'identify_startup_deferred',
    )
  })

  it('does not infer missing early groups from READY facts established under larger concurrency', async () => {
    const f = setup()
    // Shards 2 and 3 could have started in the first group when C was 8. Their
    // positive facts cannot prove 0 and 1 started when current C is only 2.
    for (const id of [2, 3])
      await f
        .gate(id, {
          initialReadySeen: () => true,
          shard: { shard_id: id, shard_count: 6 },
        })
        .publishReady(f.signal)
    const high = f.gate(4, { shard: { shard_id: 4, shard_count: 6 } })
    await expect(high.waitForIdentify(4, f.signal)).rejects.toThrow('identify_startup_deferred')
    expect(await redis().get(`${f.prefix}:state`)).toContain('"remaining":20')
  })

  it('rejects topology-mismatched saved facts before publication', async () => {
    const f = setup()
    for (const saved of [
      { ...savedCheckpoint(null), shard_id: 0, shard_count: 8 },
      { ...savedCheckpoint(null), shard_id: 1, shard_count: 4 },
      { ...savedCheckpoint({ ...session, shardCount: 8 }), shard_count: 4 },
    ]) {
      const checkpoint = new DiscordCheckpoint(
        { shard_id: 0, shard_count: 4 },
        {
          checkpoint_version: 1,
          checkpoint: saved,
        },
        discordAPIURL(),
        vi.fn(),
      )
      expect(checkpoint.initialReadySeen).toBe(false)
      await f
        .gate(0, { initialReadySeen: () => checkpoint.initialReadySeen })
        .publishReady(f.signal)
    }
    expect(await redis().exists(f.started)).toBe(0)
    await expect(f.gate(2).waitForIdentify(2, f.signal)).rejects.toThrow(
      'identify_startup_deferred',
    )
  })

  it('bounds outstanding publication and scopes a late positive write to its original topology', async () => {
    const f = setup()
    const blocked = deferred()
    const evalCommand = vi.fn<DiscordIdentifyOptions['redis']['eval']>(async (script, options) => {
      await blocked.promise
      return redis().eval(script, options)
    })
    const gate = f.gate(0, {
      redis: { eval: evalCommand, set: vi.fn() },
      initialReadySeen: () => true,
      commandTimeoutMs: 5,
    })
    for (let attempt = 0; attempt < 3; attempt++)
      await expect(gate.publishReady(f.signal)).rejects.toThrow()
    expect(evalCommand).toHaveBeenCalledOnce()
    blocked.resolve()
    await vi.waitFor(async () => {
      expect(await redis().getBit(f.started, 0)).toBe(1)
    })
    expect(await redis().exists(`${f.prefix}:started:8`)).toBe(0)
    await expect(
      f.gate(2, { shard: { shard_id: 2, shard_count: 8 } }).waitForIdentify(2, f.signal),
    ).rejects.toThrow('identify_startup_deferred')
  })
})
