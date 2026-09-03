import { beforeEach, describe, expect, it, vi } from 'vitest'

const redisMocks = vi.hoisted(() => ({
  createClient: vi.fn(),
  createCluster: vi.fn(),
}))

vi.mock('redis', () => redisMocks)

import { createGatewayRedisClient } from './redis-client'

describe('channel gateway Redis client', () => {
  beforeEach(() => {
    redisMocks.createClient.mockReset()
    redisMocks.createCluster.mockReset()
  })

  it('creates a fail-fast standalone client and delegates its lifecycle and commands', async () => {
    const raw = testRawRedisClient()
    raw.isReady = true
    redisMocks.createClient.mockReturnValue(raw)

    const client = createGatewayRedisClient({
      clusterUrls: [],
      socketTimeoutMs: 5_000,
      topology: 'standalone',
      url: 'rediss://user:secret@redis.example.com:6380/4',
    })

    expect(redisMocks.createClient).toHaveBeenCalledWith({
      commandsQueueMaxLength: 4_096,
      disableOfflineQueue: true,
      pingInterval: 2_500,
      socket: { connectTimeout: 5_000, socketTimeout: 5_000 },
      url: 'rediss://user:secret@redis.example.com:6380/4',
    })
    expect(redisMocks.createCluster).not.toHaveBeenCalled()

    const onError = vi.fn()
    client.onError(onError)
    await client.connect()
    expect(await client.ready()).toBe(true)
    expect(await client.get('key')).toBe('value')
    raw.isReady = false
    expect(await client.ready()).toBe(false)
    await client.close()
    client.destroy()

    expect(raw.on).toHaveBeenCalledWith('error', onError)
    expect(raw.connect).toHaveBeenCalledOnce()
    expect(raw.get).toHaveBeenCalledWith('key')
    expect(raw.ping).toHaveBeenCalledOnce()
    expect(raw.close).toHaveBeenCalledOnce()
    expect(raw.destroy).toHaveBeenCalledOnce()
  })

  it('creates a cluster client with common auth, TLS, and command-ready health checks', async () => {
    const raw = testRawRedisClient()
    raw.isOpen = true
    const firstMaster = testRawRedisNode()
    const secondMaster = testRawRedisNode()
    raw.masters = [{ client: firstMaster }, { client: secondMaster }]
    redisMocks.createCluster.mockReturnValue(raw)

    const client = createGatewayRedisClient({
      clusterUrls: [
        'rediss://agent:p%40ss@redis-a.example.com:6380/0',
        'rediss://agent:p%40ss@redis-b.example.com:6381',
      ],
      socketTimeoutMs: 4_000,
      topology: 'cluster',
      url: 'redis://unused.example.com:6379/0',
    })

    expect(redisMocks.createCluster).toHaveBeenCalledWith({
      defaults: {
        commandsQueueMaxLength: 4_096,
        disableOfflineQueue: true,
        password: 'p@ss',
        pingInterval: 2_000,
        socket: {
          connectTimeout: 4_000,
          socketTimeout: 4_000,
          tls: true,
        },
        username: 'agent',
      },
      rootNodes: [
        { socket: { host: 'redis-a.example.com', port: 6380 } },
        { socket: { host: 'redis-b.example.com', port: 6381 } },
      ],
    })
    expect(redisMocks.createClient).not.toHaveBeenCalled()
    expect(await client.ready()).toBe(true)
    expect(firstMaster.ping).toHaveBeenCalledOnce()
    expect(secondMaster.ping).toHaveBeenCalledOnce()

    firstMaster.isReady = false
    expect(await client.ready()).toBe(false)
    firstMaster.isReady = true
    firstMaster.ping.mockRejectedValueOnce(new Error('cluster unavailable'))
    expect(await client.ready()).toBe(false)
  })

  it('uses the primary Redis URL as the cluster seed when no seed list is configured', () => {
    redisMocks.createCluster.mockReturnValue(testRawRedisClient())

    createGatewayRedisClient({
      clusterUrls: [],
      socketTimeoutMs: 5_000,
      topology: 'cluster',
      url: 'redis://cluster.example.com:6379/0',
    })

    expect(redisMocks.createCluster).toHaveBeenCalledWith(
      expect.objectContaining({
        rootNodes: [{ socket: { host: 'cluster.example.com', port: 6379 } }],
      }),
    )
  })

  it('rejects cluster databases, mixed credentials or TLS, and URL options it cannot share', () => {
    const create = (clusterUrls: string[]) =>
      createGatewayRedisClient({
        clusterUrls,
        socketTimeoutMs: 5_000,
        topology: 'cluster',
        url: 'redis://unused.example.com:6379/0',
      })

    expect(() => create(['redis://redis-a:6379/1'])).toThrow(
      'Redis Cluster supports only database 0',
    )
    expect(() =>
      create(['redis://user:one@redis-a:6379/0', 'redis://user:two@redis-b:6379/0']),
    ).toThrow('same protocol and credentials')
    expect(() => create(['redis://redis-a:6379/0', 'rediss://redis-b:6379/0'])).toThrow(
      'same protocol and credentials',
    )
    expect(() => create(['redis://redis-a:6379/0?option=value'])).toThrow(
      'must not contain a query or fragment',
    )
    expect(redisMocks.createCluster).not.toHaveBeenCalled()
  })
})

function testRawRedisClient() {
  return {
    close: vi.fn(() => Promise.resolve()),
    connect: vi.fn(() => Promise.resolve()),
    del: vi.fn(() => Promise.resolve(0)),
    destroy: vi.fn(),
    eval: vi.fn(() => Promise.resolve<unknown>(1)),
    exists: vi.fn(() => Promise.resolve(0)),
    get: vi.fn(() => Promise.resolve<string | null>('value')),
    isOpen: false,
    isReady: false,
    lLen: vi.fn(() => Promise.resolve(0)),
    lPop: vi.fn(() => Promise.resolve<string | null>(null)),
    lRange: vi.fn(() => Promise.resolve<string[]>([])),
    masters: [] as { client?: ReturnType<typeof testRawRedisNode> }[],
    on: vi.fn(),
    ping: vi.fn(() => Promise.resolve('PONG')),
    sAdd: vi.fn(() => Promise.resolve(1)),
    sIsMember: vi.fn(() => Promise.resolve(0)),
    sRem: vi.fn(() => Promise.resolve(1)),
    set: vi.fn(() => Promise.resolve<string | null>('OK')),
    unlink: vi.fn(() => Promise.resolve(0)),
  }
}

function testRawRedisNode() {
  return {
    isReady: true,
    ping: vi.fn(() => Promise.resolve('PONG')),
  }
}
