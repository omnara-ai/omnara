import {
  createClient,
  createCluster,
  type RedisClientOptions,
  type RedisClientType,
  type RedisClusterOptions,
  type RedisClusterType,
} from 'redis'

import type { RedisStateClient } from './app-state'
import type { RedisTopology } from './config'

const commandQueueMaxLength = 4_096

export interface GatewayRedisClient extends RedisStateClient {
  close(): Promise<void>
  connect(): Promise<void>
  destroy(): void
  onError(listener: (error: Error) => void): void
  ready(): Promise<boolean>
}

export interface GatewayRedisClientOptions {
  clusterUrls: string[]
  socketTimeoutMs: number
  topology: RedisTopology
  url: string
}

export interface GatewayRedisFactories {
  createClient(options: RedisClientOptions): RedisStandaloneConnection
  createCluster(options: RedisClusterOptions): RedisClusterConnection
}

export function createGatewayRedisClient(
  options: GatewayRedisClientOptions,
  factories: GatewayRedisFactories = { createClient, createCluster },
): GatewayRedisClient {
  const socketSafety = {
    connectTimeout: options.socketTimeoutMs,
    socketTimeout: options.socketTimeoutMs,
  }
  const commandSafety = {
    commandsQueueMaxLength: commandQueueMaxLength,
    disableOfflineQueue: true,
    pingInterval: Math.max(50, Math.floor(options.socketTimeoutMs / 2)),
  }

  if (options.topology === 'standalone') {
    const client = factories.createClient({
      ...commandSafety,
      socket: socketSafety,
      url: options.url,
    })
    return new ManagedRedisClient(client, client, async () => {
      if (!client.isReady) return false
      await client.ping()
      return true
    })
  }

  const seeds = parseClusterSeeds(
    options.clusterUrls.length > 0 ? options.clusterUrls : [options.url],
  )
  const credentials = seeds[0]
  const socket =
    credentials.protocol === 'rediss:'
      ? { ...socketSafety, tls: true as const }
      : { ...socketSafety, tls: false as const }
  const defaults: RedisClusterOptions['defaults'] = { ...commandSafety, socket }
  if (credentials.password !== undefined) defaults.password = credentials.password
  if (credentials.username !== undefined) defaults.username = credentials.username
  const client = factories.createCluster({
    defaults,
    rootNodes: seeds.map(({ host, port }) => ({ socket: { host, port } })),
  })
  return new ManagedRedisClient(client, client, async () => {
    if (!client.isOpen || client.masters.length === 0) return false
    const probes: Promise<string>[] = []
    for (const master of client.masters) {
      if (!master.client?.isReady) return false
      probes.push(master.client.ping())
    }
    await Promise.all(probes)
    return true
  })
}

interface RedisLifecycle extends Pick<RedisClientType, 'close' | 'destroy'> {
  // Only completion is consumed; Redis returns its fluent client while other
  // implementations may complete without a value. Event registration is also ignored.
  connect(): Promise<RedisLifecycle> | Promise<void>
  on(event: 'error', listener: (error: Error) => void): void
}

interface RedisStandaloneConnection
  extends RedisLifecycle, RedisStateClient, Pick<RedisClientType, 'isReady' | 'ping'> {}

interface RedisMaster {
  readonly client?: Pick<RedisClientType, 'isReady' | 'ping'>
}

interface RedisClusterConnection
  extends RedisLifecycle, RedisStateClient, Pick<RedisClusterType, 'isOpen'> {
  readonly masters: readonly RedisMaster[]
}

class ManagedRedisClient implements GatewayRedisClient {
  constructor(
    private readonly lifecycle: RedisLifecycle,
    private readonly state: RedisStateClient,
    private readonly readinessProbe: () => Promise<boolean>,
  ) {}

  async close(): Promise<void> {
    await this.lifecycle.close()
  }

  async connect(): Promise<void> {
    await this.lifecycle.connect()
  }

  destroy(): void {
    this.lifecycle.destroy()
  }

  onError(listener: (error: Error) => void): void {
    this.lifecycle.on('error', listener)
  }

  async ready(): Promise<boolean> {
    try {
      return await this.readinessProbe()
    } catch {
      return false
    }
  }

  del(key: string): Promise<number> {
    return this.state.del(key)
  }

  eval(
    script: string,
    options: { arguments: string[]; keys: string[] },
  ): ReturnType<RedisStateClient['eval']> {
    return this.state.eval(script, options)
  }

  exists(key: string): Promise<number> {
    return this.state.exists(key)
  }

  get(key: string): Promise<string | null> {
    return this.state.get(key)
  }

  lLen(key: string): Promise<number> {
    return this.state.lLen(key)
  }

  lPop(key: string): Promise<string | null> {
    return this.state.lPop(key)
  }

  lRange(key: string, start: number, stop: number): Promise<string[]> {
    return this.state.lRange(key, start, stop)
  }

  sAdd(key: string, member: string): Promise<number> {
    return this.state.sAdd(key, member)
  }

  sIsMember(key: string, member: string): Promise<number> {
    return this.state.sIsMember(key, member)
  }

  sRem(key: string, member: string): Promise<number> {
    return this.state.sRem(key, member)
  }

  set(key: string, value: string, options?: { NX?: boolean; PX?: number }): Promise<string | null> {
    return this.state.set(key, value, options)
  }

  unlink(key: string): Promise<number> {
    return this.state.unlink(key)
  }
}

interface ClusterSeed {
  host: string
  password?: string
  port: number
  protocol: 'redis:' | 'rediss:'
  username?: string
}

function parseClusterSeeds(values: string[]): [ClusterSeed, ...ClusterSeed[]] {
  if (values.length === 0) throw new Error('at least one Redis cluster seed URL is required')
  const [firstValue, ...remainingValues] = values
  if (!firstValue) throw new Error('at least one Redis cluster seed URL is required')
  const first = parseClusterSeed(firstValue)
  const seeds: [ClusterSeed, ...ClusterSeed[]] = [first, ...remainingValues.map(parseClusterSeed)]
  for (const seed of seeds.slice(1)) {
    if (
      seed.protocol !== first.protocol ||
      seed.username !== first.username ||
      seed.password !== first.password
    ) {
      throw new Error('Redis cluster seed URLs must use the same protocol and credentials')
    }
  }
  return seeds
}

function parseClusterSeed(value: string): ClusterSeed {
  const url = new URL(value)
  if (url.protocol !== 'redis:' && url.protocol !== 'rediss:') {
    throw new Error('Redis cluster seed URLs must use redis or rediss')
  }
  if (url.search || url.hash) {
    throw new Error('Redis cluster seed URLs must not contain a query or fragment')
  }
  const database = url.pathname === '' || url.pathname === '/' ? 0 : Number(url.pathname.slice(1))
  if (!Number.isSafeInteger(database) || database !== 0) {
    throw new Error('Redis Cluster supports only database 0')
  }
  const seed: ClusterSeed = {
    host: url.hostname.replace(/^\[([0-9a-f:]+)\]$/i, '$1'),
    port: url.port === '' ? 6379 : Number(url.port),
    protocol: url.protocol,
  }
  if (url.password !== '') seed.password = decodeURIComponent(url.password)
  if (url.username !== '') seed.username = decodeURIComponent(url.username)
  return seed
}
