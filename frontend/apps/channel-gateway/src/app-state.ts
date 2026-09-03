import type { Lock, QueueEntry, StateAdapter } from 'chat'

const subscriptionShardCount = 256
const cleanupConcurrency = 16
const knownAppMarkerTtlMs = 30 * 24 * 60 * 60 * 1_000
const appStateMaxTtlMs = knownAppMarkerTtlMs

const releaseLockScript = `
  if redis.call("get", KEYS[1]) == ARGV[1] then
    return redis.call("del", KEYS[1])
  end
  return 0
`

const extendLockScript = `
  if redis.call("get", KEYS[1]) == ARGV[1] then
    return redis.call("pexpire", KEYS[1], ARGV[2])
  end
  return 0
`

const appendListScript = `
  redis.call("rpush", KEYS[1], ARGV[1])
  if tonumber(ARGV[2]) > 0 then
    redis.call("ltrim", KEYS[1], -tonumber(ARGV[2]), -1)
  end
  if tonumber(ARGV[3]) > 0 then
    redis.call("pexpire", KEYS[1], tonumber(ARGV[3]))
  end
  return 1
`

const enqueueScript = `
  redis.call("rpush", KEYS[1], ARGV[1])
  if tonumber(ARGV[2]) > 0 then
    redis.call("ltrim", KEYS[1], -tonumber(ARGV[2]), -1)
  end
  redis.call("pexpire", KEYS[1], ARGV[3])
  return redis.call("llen", KEYS[1])
`

export interface RedisStateClient {
  del(key: string): Promise<number>
  eval(script: string, options: { arguments: string[]; keys: string[] }): Promise<unknown>
  exists(key: string): Promise<number>
  get(key: string): Promise<string | null>
  lLen(key: string): Promise<number>
  lPop(key: string): Promise<string | null>
  lRange(key: string, start: number, stop: number): Promise<string[]>
  sAdd(key: string, member: string): Promise<number>
  sIsMember(key: string, member: string): Promise<number>
  sRem(key: string, member: string): Promise<number>
  set(key: string, value: string, options?: { NX?: boolean; PX?: number }): Promise<string | null>
  unlink(key: string): Promise<number>
}

export interface AppStateFactory {
  clearSubscriptions(integrationAppId: string): Promise<void>
  forApp(integrationAppId: string): StateAdapter
  markKnownApp(integrationAppId: string): Promise<void>
}

export interface RedisAppStateFactoryOptions {
  client: RedisStateClient
  keyPrefix: string
}

/**
 * Implements Chat SDK state with cluster-safe, single-key commands. Every app
 * has an independent keyspace, and durable subscriptions are split into fixed
 * shards instead of the official adapter's one global set.
 */
export class RedisAppStateFactory implements AppStateFactory {
  private readonly keyPrefix: string

  constructor(private readonly options: RedisAppStateFactoryOptions) {
    this.keyPrefix = options.keyPrefix.trim()
    if (this.keyPrefix === '') throw new Error('state key prefix is required')
  }

  forApp(integrationAppId: string): StateAdapter {
    return new AppScopedStateAdapter(this.options.client, this.appPrefix(integrationAppId))
  }

  async clearSubscriptions(integrationAppId: string): Promise<void> {
    const prefix = this.appPrefix(integrationAppId)
    const marker = knownAppKey(prefix)
    if ((await this.options.client.exists(marker)) === 0) return
    await this.options.client.set(marker, 'v1', { PX: knownAppMarkerTtlMs })

    const failures: unknown[] = []
    for (let offset = 0; offset < subscriptionShardCount; offset += cleanupConcurrency) {
      const deletions: Promise<number>[] = []
      for (
        let shard = offset;
        shard < Math.min(offset + cleanupConcurrency, subscriptionShardCount);
        shard += 1
      ) {
        deletions.push(this.options.client.unlink(subscriptionKey(prefix, shard)))
      }
      for (const result of await Promise.allSettled(deletions)) {
        if (result.status === 'rejected') failures.push(result.reason)
      }
    }
    if (failures.length > 0) {
      throw new AggregateError(failures, 'failed to clear one or more app subscription shards')
    }
  }

  async markKnownApp(integrationAppId: string): Promise<void> {
    const prefix = this.appPrefix(integrationAppId)
    await this.options.client.set(knownAppKey(prefix), 'v1', { PX: knownAppMarkerTtlMs })
  }

  private appPrefix(integrationAppId: string): string {
    const id = integrationAppId.trim()
    if (id === '') throw new Error('integration app id is required')
    return `${this.keyPrefix}:app:${id.length}:${id}`
  }
}

class AppScopedStateAdapter implements StateAdapter {
  constructor(
    private readonly client: RedisStateClient,
    private readonly prefix: string,
  ) {}

  connect(): Promise<void> {
    return Promise.resolve()
  }

  disconnect(): Promise<void> {
    // The gateway owns the shared Redis adapter and connection.
    return Promise.resolve()
  }

  async acquireLock(threadId: string, ttlMs: number): Promise<Lock | null> {
    const token = crypto.randomUUID()
    const acquired = await this.client.set(this.key('lock', threadId), token, {
      NX: true,
      PX: ttlMs,
    })
    return acquired === 'OK' ? { expiresAt: Date.now() + ttlMs, threadId, token } : null
  }

  async appendToList(
    key: string,
    value: unknown,
    options?: { maxLength?: number; ttlMs?: number },
  ): Promise<void> {
    await this.client.eval(appendListScript, {
      arguments: [
        serialized(value),
        String(options?.maxLength ?? 0),
        String(appStateTtl(options?.ttlMs)),
      ],
      keys: [this.key('list', key)],
    })
  }

  async delete(key: string): Promise<void> {
    await this.client.del(this.key('cache', key))
  }

  async dequeue(threadId: string): Promise<QueueEntry | null> {
    const value = await this.client.lPop(this.key('queue', threadId))
    return value === null ? null : (JSON.parse(value) as QueueEntry)
  }

  async enqueue(threadId: string, entry: QueueEntry, maxSize: number): Promise<number> {
    const result = await this.client.eval(enqueueScript, {
      arguments: [
        serialized(entry),
        String(maxSize),
        String(Math.max(entry.expiresAt - Date.now(), 60_000)),
      ],
      keys: [this.key('queue', threadId)],
    })
    const depth = Number(result)
    if (!Number.isSafeInteger(depth) || depth < 0) {
      throw new Error('Redis returned an invalid Chat SDK queue depth')
    }
    return depth
  }

  async extendLock(lock: Lock, ttlMs: number): Promise<boolean> {
    const result = await this.client.eval(extendLockScript, {
      arguments: [lock.token, String(ttlMs)],
      keys: [this.key('lock', lock.threadId)],
    })
    return result === 1
  }

  async forceReleaseLock(threadId: string): Promise<void> {
    await this.client.del(this.key('lock', threadId))
  }

  async get<T = unknown>(key: string): Promise<T | null> {
    const value = await this.client.get(this.key('cache', key))
    if (value === null) return null
    try {
      return JSON.parse(value) as T
    } catch {
      return value as T
    }
  }

  async getList<T = unknown>(key: string): Promise<T[]> {
    const values = await this.client.lRange(this.key('list', key), 0, -1)
    return values.map((value) => JSON.parse(value) as T)
  }

  isSubscribed(threadId: string): Promise<boolean> {
    return this.client
      .sIsMember(this.threadSubscriptionKey(threadId), threadId)
      .then((result) => result === 1)
  }

  queueDepth(threadId: string): Promise<number> {
    return this.client.lLen(this.key('queue', threadId))
  }

  async releaseLock(lock: Lock): Promise<void> {
    await this.client.eval(releaseLockScript, {
      arguments: [lock.token],
      keys: [this.key('lock', lock.threadId)],
    })
  }

  async set(key: string, value: unknown, ttlMs?: number): Promise<void> {
    await this.client.set(this.key('cache', key), serialized(value), { PX: appStateTtl(ttlMs) })
  }

  async setIfNotExists(key: string, value: unknown, ttlMs?: number): Promise<boolean> {
    const result = await this.client.set(this.key('cache', key), serialized(value), {
      NX: true,
      PX: appStateTtl(ttlMs),
    })
    return result === 'OK'
  }

  async subscribe(threadId: string): Promise<void> {
    await this.client.sAdd(this.threadSubscriptionKey(threadId), threadId)
  }

  async unsubscribe(threadId: string): Promise<void> {
    await this.client.sRem(this.threadSubscriptionKey(threadId), threadId)
  }

  private key(kind: 'cache' | 'list' | 'lock' | 'queue', value: string): string {
    return `${this.prefix}:${kind}:${value}`
  }

  private threadSubscriptionKey(threadId: string): string {
    return subscriptionKey(this.prefix, subscriptionShard(threadId))
  }
}

function subscriptionKey(prefix: string, shard: number): string {
  return `${prefix}:subscriptions:v1:${shard.toString(16).padStart(2, '0')}`
}

function knownAppKey(prefix: string): string {
  return `${prefix}:known:v1`
}

function serialized(value: unknown): string {
  const result = JSON.stringify(value) as string | undefined
  if (result === undefined) throw new Error('Chat SDK state value is not JSON-serializable')
  return result
}

function appStateTtl(requestedMs?: number): number {
  if (requestedMs === undefined) return appStateMaxTtlMs
  if (!Number.isSafeInteger(requestedMs) || requestedMs <= 0) {
    throw new Error('Chat SDK state TTL must be a positive integer')
  }
  return Math.min(requestedMs, appStateMaxTtlMs)
}

function subscriptionShard(threadId: string): number {
  let hash = 2_166_136_261
  for (let index = 0; index < threadId.length; index += 1) {
    hash ^= threadId.charCodeAt(index)
    hash = Math.imul(hash, 16_777_619)
  }
  return (hash >>> 0) % subscriptionShardCount
}
