import type { QueueEntry } from 'chat'
import { describe, expect, it, vi } from 'vitest'

import { RedisAppStateFactory, type RedisStateClient } from './app-state'

const appPrefix = 'omnara:chat-sdk:app:5:app-a'

describe('app-scoped Chat SDK state', () => {
  it('places each app subscription into an independent physical Redis shard', async () => {
    const client = testRedisClient()
    const factory = testFactory(client)

    await factory.forApp('app-a').subscribe('discord:thread-1')
    await factory.forApp('app-a').subscribe('discord:thread-2')
    await factory.forApp('app-b').subscribe('discord:thread-1')

    const calls = client.sAdd.mock.calls
    expect(calls.map(([key]) => key)).toEqual([
      expect.stringMatching(/^omnara:chat-sdk:app:5:app-a:subscriptions:v1:[0-9a-f]{2}$/),
      expect.stringMatching(/^omnara:chat-sdk:app:5:app-a:subscriptions:v1:[0-9a-f]{2}$/),
      expect.stringMatching(/^omnara:chat-sdk:app:5:app-b:subscriptions:v1:[0-9a-f]{2}$/),
    ])
    expect(calls[0]?.[0]).not.toBe(calls[1]?.[0])
    expect(calls[0]?.[0]).not.toBe(calls[2]?.[0])
    expect(calls.map((call) => call[1])).toEqual([
      'discord:thread-1',
      'discord:thread-2',
      'discord:thread-1',
    ])
  })

  it('uses the same shard for subscription membership and removal', async () => {
    const client = testRedisClient()
    client.sIsMember.mockResolvedValue(1)
    const scoped = testFactory(client).forApp('app-a')

    expect(await scoped.isSubscribed('slack:thread-1')).toBe(true)
    await scoped.unsubscribe('slack:thread-1')

    expect(client.sIsMember.mock.calls[0]?.[0]).toBe(client.sRem.mock.calls[0]?.[0])
    expect(client.sRem).toHaveBeenCalledWith(
      expect.stringMatching(/:subscriptions:v1:[0-9a-f]{2}$/),
      'slack:thread-1',
    )
  })

  it('records a durable known-app marker only when explicitly asked', async () => {
    const client = testRedisClient()
    const factory = testFactory(client)

    factory.forApp('app-a')
    expect(client.set).not.toHaveBeenCalled()

    await factory.markKnownApp('app-a')

    expect(client.set).toHaveBeenCalledWith(`${appPrefix}:known:v1`, 'v1', {
      PX: 30 * 24 * 60 * 60 * 1_000,
    })
  })

  it('does constant work for an unknown app instead of sweeping attacker-chosen keys', async () => {
    const client = testRedisClient()

    await testFactory(client).clearSubscriptions('app-a')

    expect(client.exists).toHaveBeenCalledOnce()
    expect(client.exists).toHaveBeenCalledWith(`${appPrefix}:known:v1`)
    expect(client.unlink).not.toHaveBeenCalled()
  })

  it('unlinks every known app subscription shard without deleting its marker', async () => {
    const client = testRedisClient()
    client.exists.mockResolvedValue(1)

    await testFactory(client).clearSubscriptions('app-a')

    const keys = client.unlink.mock.calls.map(([key]) => key)
    expect(keys).toHaveLength(256)
    expect(new Set(keys).size).toBe(256)
    expect(keys[0]).toBe(`${appPrefix}:subscriptions:v1:00`)
    expect(keys[255]).toBe(`${appPrefix}:subscriptions:v1:ff`)
    expect(keys).not.toContain(`${appPrefix}:known:v1`)
    expect(client.del).not.toHaveBeenCalledWith(`${appPrefix}:known:v1`)
    expect(client.set).toHaveBeenCalledWith(`${appPrefix}:known:v1`, 'v1', {
      PX: 30 * 24 * 60 * 60 * 1_000,
    })
  })

  it('attempts every shard and preserves the marker when part of cleanup fails', async () => {
    const client = testRedisClient()
    client.exists.mockResolvedValue(1)
    client.unlink.mockImplementation((key: string) =>
      key.endsWith(':07') || key.endsWith(':f1')
        ? Promise.reject(new Error(`cannot unlink ${key}`))
        : Promise.resolve(0),
    )
    const factory = testFactory(client)

    await expect(factory.clearSubscriptions('app-a')).rejects.toThrow(
      'failed to clear one or more app subscription shards',
    )

    expect(client.unlink).toHaveBeenCalledTimes(256)
    expect(client.unlink).not.toHaveBeenCalledWith(`${appPrefix}:known:v1`)

    client.unlink.mockClear()
    client.unlink.mockResolvedValue(0)
    await factory.clearSubscriptions('app-a')
    expect(client.unlink).toHaveBeenCalledTimes(256)
  })

  it('keeps a tombstone so a later cleanup catches a stale gateway subscription', async () => {
    const client = testRedisClient()
    client.exists.mockResolvedValue(1)
    const factory = testFactory(client)

    await factory.clearSubscriptions('app-a')
    expect(client.unlink).toHaveBeenCalledTimes(256)

    await factory.forApp('app-a').subscribe('discord:late-thread')
    const lateSubscriptionKey = client.sAdd.mock.calls[0]?.[0]
    expect(lateSubscriptionKey).toMatch(/:subscriptions:v1:[0-9a-f]{2}$/)

    client.unlink.mockClear()
    await factory.clearSubscriptions('app-a')
    expect(client.unlink).toHaveBeenCalledTimes(256)
    expect(client.unlink).toHaveBeenCalledWith(lateSubscriptionKey)
    expect(client.del).not.toHaveBeenCalledWith(`${appPrefix}:known:v1`)
    expect(client.set).toHaveBeenLastCalledWith(`${appPrefix}:known:v1`, 'v1', {
      PX: 30 * 24 * 60 * 60 * 1_000,
    })
  })

  it('implements locks with one-key compare-and-release scripts', async () => {
    const client = testRedisClient()
    const scoped = testFactory(client).forApp('app-a')

    const lock = await scoped.acquireLock('slack:thread-1', 1_000)
    if (!lock) throw new Error('expected lock')
    expect(lock.expiresAt).toBeGreaterThan(Date.now())
    expect(lock.threadId).toBe('slack:thread-1')
    expect(typeof lock.token).toBe('string')
    expect(client.set).toHaveBeenCalledWith(`${appPrefix}:lock:slack:thread-1`, lock.token, {
      NX: true,
      PX: 1_000,
    })

    client.eval.mockResolvedValueOnce(1)
    expect(await scoped.extendLock(lock, 2_000)).toBe(true)
    await scoped.releaseLock(lock)
    await scoped.forceReleaseLock(lock.threadId)

    for (const [, options] of client.eval.mock.calls) {
      expect(options.keys).toEqual([`${appPrefix}:lock:slack:thread-1`])
    }
    expect(client.del).toHaveBeenCalledWith(`${appPrefix}:lock:slack:thread-1`)
  })

  it('implements cache and list state with app-scoped keys', async () => {
    const client = testRedisClient()
    const scoped = testFactory(client).forApp('app-a')

    await scoped.set('history', { count: 2 }, 5_000)
    expect(client.set).toHaveBeenCalledWith(`${appPrefix}:cache:history`, '{"count":2}', {
      PX: 5_000,
    })

    client.get.mockResolvedValueOnce('{"count":2}').mockResolvedValueOnce('legacy-text')
    expect(await scoped.get('history')).toEqual({ count: 2 })
    expect(await scoped.get('history')).toBe('legacy-text')

    client.set.mockResolvedValueOnce(null)
    expect(await scoped.setIfNotExists('history', 3)).toBe(false)
    expect(client.set).toHaveBeenLastCalledWith(`${appPrefix}:cache:history`, '3', {
      NX: true,
      PX: 30 * 24 * 60 * 60 * 1_000,
    })
    await scoped.delete('history')
    expect(client.del).toHaveBeenCalledWith(`${appPrefix}:cache:history`)

    await scoped.appendToList('messages', { id: 1 }, { maxLength: 10, ttlMs: 60_000 })
    client.lRange.mockResolvedValueOnce(['{"id":1}', '{"id":2}'])
    expect(await scoped.getList('messages')).toEqual([{ id: 1 }, { id: 2 }])
    const [, appendOptions] = client.eval.mock.calls.at(-1) ?? []
    expect(appendOptions).toEqual({
      arguments: ['{"id":1}', '10', '60000'],
      keys: [`${appPrefix}:list:messages`],
    })
  })

  it('bounds app cache and list retention', async () => {
    const client = testRedisClient()
    const scoped = testFactory(client).forApp('app-a')
    const maxTtl = 30 * 24 * 60 * 60 * 1_000

    await scoped.set('default-cache', 1)
    await scoped.set('short-cache', 2, 5_000)
    await scoped.setIfNotExists('bounded-cache', 3, maxTtl + 1)
    await scoped.appendToList('default-list', 4)
    await scoped.appendToList('bounded-list', 5, { ttlMs: maxTtl + 1 })

    expect(client.set).toHaveBeenNthCalledWith(1, `${appPrefix}:cache:default-cache`, '1', {
      PX: maxTtl,
    })
    expect(client.set).toHaveBeenNthCalledWith(2, `${appPrefix}:cache:short-cache`, '2', {
      PX: 5_000,
    })
    expect(client.set).toHaveBeenNthCalledWith(3, `${appPrefix}:cache:bounded-cache`, '3', {
      NX: true,
      PX: maxTtl,
    })
    expect(client.eval.mock.calls.at(-2)?.[1]).toEqual({
      arguments: ['4', '0', String(maxTtl)],
      keys: [`${appPrefix}:list:default-list`],
    })
    expect(client.eval.mock.calls.at(-1)?.[1]).toEqual({
      arguments: ['5', '0', String(maxTtl)],
      keys: [`${appPrefix}:list:bounded-list`],
    })

    await expect(scoped.set('zero', 1, 0)).rejects.toThrow('TTL must be a positive integer')
    await expect(scoped.setIfNotExists('negative', 1, -1)).rejects.toThrow(
      'TTL must be a positive integer',
    )
    await expect(scoped.appendToList('zero-list', 1, { ttlMs: 0 })).rejects.toThrow(
      'TTL must be a positive integer',
    )
  })

  it('implements the pending queue with one app-scoped Redis key', async () => {
    const now = vi.spyOn(Date, 'now').mockReturnValue(1_000)
    try {
      const client = testRedisClient()
      const scoped = testFactory(client).forApp('app-a')
      const entry = {
        enqueuedAt: 1_000,
        expiresAt: 91_000,
        message: { id: 'message-1' },
      } as unknown as QueueEntry
      client.eval.mockResolvedValueOnce(2)

      expect(await scoped.enqueue('discord:thread-1', entry, 10)).toBe(2)
      expect(client.eval.mock.calls[0]?.[1]).toEqual({
        arguments: [JSON.stringify(entry), '10', '90000'],
        keys: [`${appPrefix}:queue:discord:thread-1`],
      })

      client.lPop.mockResolvedValueOnce(JSON.stringify(entry))
      client.lLen.mockResolvedValueOnce(1)
      expect(await scoped.dequeue('discord:thread-1')).toEqual(entry)
      expect(await scoped.queueDepth('discord:thread-1')).toBe(1)
      expect(client.lPop).toHaveBeenCalledWith(`${appPrefix}:queue:discord:thread-1`)
      expect(client.lLen).toHaveBeenCalledWith(`${appPrefix}:queue:discord:thread-1`)
    } finally {
      now.mockRestore()
    }
  })

  it('rejects empty app identities and values JSON cannot represent', async () => {
    const factory = testFactory(testRedisClient())
    expect(() => factory.forApp('  ')).toThrow('integration app id is required')
    await expect(factory.forApp('app-a').set('value', undefined)).rejects.toThrow(
      'not JSON-serializable',
    )
  })
})

function testFactory(client: TestRedisClient): RedisAppStateFactory {
  return new RedisAppStateFactory({ client, keyPrefix: 'omnara:chat-sdk' })
}

type TestRedisClient = ReturnType<typeof testRedisClient>

function testRedisClient() {
  return {
    del: vi.fn<RedisStateClient['del']>(() => Promise.resolve(0)),
    eval: vi.fn<RedisStateClient['eval']>(() => Promise.resolve<unknown>(1)),
    exists: vi.fn<RedisStateClient['exists']>(() => Promise.resolve(0)),
    get: vi.fn<RedisStateClient['get']>(() => Promise.resolve<string | null>(null)),
    lLen: vi.fn<RedisStateClient['lLen']>(() => Promise.resolve(0)),
    lPop: vi.fn<RedisStateClient['lPop']>(() => Promise.resolve<string | null>(null)),
    lRange: vi.fn<RedisStateClient['lRange']>(() => Promise.resolve<string[]>([])),
    sAdd: vi.fn<RedisStateClient['sAdd']>(() => Promise.resolve(1)),
    sIsMember: vi.fn<RedisStateClient['sIsMember']>(() => Promise.resolve(0)),
    sRem: vi.fn<RedisStateClient['sRem']>(() => Promise.resolve(1)),
    set: vi.fn<RedisStateClient['set']>(() => Promise.resolve<string | null>('OK')),
    unlink: vi.fn<RedisStateClient['unlink']>(() => Promise.resolve(0)),
  } satisfies RedisStateClient
}
