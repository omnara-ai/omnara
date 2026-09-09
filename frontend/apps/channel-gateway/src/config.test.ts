import { describe, expect, it } from 'vitest'

import { loadConfig } from './config'

const validEnv: NodeJS.ProcessEnv = {
  OMNARA_CHANNEL_CORE_API_URL: 'http://api:8080/api/v1/',
  OMNARA_CHANNEL_CONNECTOR_TOKEN: 'test-token',
  OMNARA_CHANNEL_GATEWAY_PUBLIC_URL: 'https://channels.example.com',
  OMNARA_CHANNEL_REDIS_TOPOLOGY: 'standalone',
  OMNARA_CHANNEL_REDIS_URL: 'redis://redis:6379/0',
}

describe('channel gateway configuration', () => {
  it('requires an explicit Redis topology for the independently deployed gateway', () => {
    const env = { ...validEnv }
    delete env.OMNARA_CHANNEL_REDIS_TOPOLOGY
    expect(() => loadConfig(env)).toThrow('OMNARA_CHANNEL_REDIS_TOPOLOGY is required')
  })

  it('normalizes self-hosted API, public gateway, and Redis URLs', () => {
    const config = loadConfig(validEnv)
    expect(config.apiBaseUrl).toBe('http://api:8080/api/v1')
    expect(config.publicUrl).toBe('https://channels.example.com')
    expect(config.redisUrl).toBe('redis://redis:6379/0')
    expect(config.redisTopology).toBe('standalone')
    expect(config.redisClusterUrls).toEqual([])
    expect(config.redisSocketTimeoutMs).toBe(5_000)
    expect(config.deliverySendTimeoutMs + config.deliveryCompletionTimeoutMs).toBeLessThan(
      config.deliveryLeaseMs,
    )
    expect(config.startupTimeoutMs).toBe(30_000)
  })

  it('rejects a public URL with a path so provider signatures use one trusted origin', () => {
    expect(() =>
      loadConfig({
        ...validEnv,
        OMNARA_CHANNEL_GATEWAY_PUBLIC_URL: 'https://channels.example.com/untrusted-path',
      }),
    ).toThrow('must be a public origin')
  })

  it('rejects a send timeout that can outlive its delivery lease', () => {
    expect(() =>
      loadConfig({
        ...validEnv,
        OMNARA_CHANNEL_DELIVERY_LEASE_MS: '1000',
        OMNARA_CHANNEL_DELIVERY_SEND_TIMEOUT_MS: '1000',
      }),
    ).toThrow('send timeout, completion timeout, and safety margin must fit')
  })

  it('accepts rediss and rejects non-Redis state endpoints', () => {
    expect(
      loadConfig({ ...validEnv, OMNARA_CHANNEL_REDIS_URL: 'rediss://redis.example.com' }).redisUrl,
    ).toBe('rediss://redis.example.com')
    expect(() =>
      loadConfig({ ...validEnv, OMNARA_CHANNEL_REDIS_URL: 'https://redis.example.com' }),
    ).toThrow('must use redis or rediss')
  })

  it('parses explicit cluster seed URLs and rejects topology mistakes', () => {
    expect(
      loadConfig({
        ...validEnv,
        OMNARA_CHANNEL_REDIS_CLUSTER_URLS:
          'rediss://user:secret@redis-a.example.com:6379/0, rediss://user:secret@redis-b.example.com:6379/0',
        OMNARA_CHANNEL_REDIS_TOPOLOGY: 'cluster',
      }).redisClusterUrls,
    ).toEqual([
      'rediss://user:secret@redis-a.example.com:6379/0',
      'rediss://user:secret@redis-b.example.com:6379/0',
    ])
    expect(() =>
      loadConfig({
        ...validEnv,
        OMNARA_CHANNEL_REDIS_CLUSTER_URLS: 'redis://redis-a:6379/0',
        OMNARA_CHANNEL_REDIS_TOPOLOGY: 'standalone',
      }),
    ).toThrow('requires OMNARA_CHANNEL_REDIS_TOPOLOGY=cluster')
    expect(() => loadConfig({ ...validEnv, OMNARA_CHANNEL_REDIS_TOPOLOGY: 'sentinel' })).toThrow(
      'must be standalone or cluster',
    )
  })

  it('bounds Redis socket work within the webhook lifecycle', () => {
    expect(() =>
      loadConfig({
        ...validEnv,
        OMNARA_CHANNEL_REDIS_SOCKET_TIMEOUT_MS: '6000',
        OMNARA_CHANNEL_WEBHOOK_HANDLER_TIMEOUT_MS: '5000',
      }),
    ).toThrow('must not exceed OMNARA_CHANNEL_WEBHOOK_HANDLER_TIMEOUT_MS')
  })

  it('bounds the configured instance ID used in lease ownership records', () => {
    expect(
      loadConfig({
        ...validEnv,
        OMNARA_CHANNEL_GATEWAY_INSTANCE_ID: 'a'.repeat(127),
      }).instanceId,
    ).toHaveLength(127)
    expect(() =>
      loadConfig({
        ...validEnv,
        OMNARA_CHANNEL_GATEWAY_INSTANCE_ID: 'a'.repeat(128),
      }),
    ).toThrow('1-127 ASCII')
    expect(() =>
      loadConfig({
        ...validEnv,
        OMNARA_CHANNEL_GATEWAY_INSTANCE_ID: 'gateway/one',
      }),
    ).toThrow('1-127 ASCII')
  })
})
