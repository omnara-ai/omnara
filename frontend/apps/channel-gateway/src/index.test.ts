import { describe, expect, it, vi } from 'vitest'

import type { RedisStateClient } from './app-state'
import { loadConfig } from './config'
import { unexpectedTestCall } from './gateway-test-fixtures'
import {
  createProviderFactoryRegistry,
  providerFactoryCapabilities,
  runGateway,
  startClaimLoops,
} from './index'
import type { GatewayRedisClient } from './redis-client'
import { GatewayServer } from './server'
import type { GatewayLogger, ProviderFactory, ProviderFactoryRegistry } from './types'

describe('channel gateway provider capabilities', () => {
  it('does not start claim loops when no provider adapter is registered', () => {
    const deliveryLoop = { run: vi.fn(() => Promise.resolve()) }
    const runtimeLoop = { run: vi.fn(() => Promise.resolve()) }

    expect(
      startClaimLoops(new Map(), deliveryLoop, runtimeLoop, new AbortController().signal),
    ).toEqual([])
    expect(deliveryLoop.run).not.toHaveBeenCalled()
    expect(runtimeLoop.run).not.toHaveBeenCalled()
  })

  it('derives exact claim capabilities from the adapters in this binary', () => {
    const factories = new Map<string, ProviderFactory>([
      [
        'chat_sdk_v1/discord',
        {
          connectorKey: 'chat_sdk_v1',
          provider: 'discord',
          create: vi.fn(),
        },
      ],
      [
        'custom/github',
        {
          connectorKey: 'custom',
          provider: 'github',
          create: vi.fn(),
        },
      ],
    ]) satisfies ProviderFactoryRegistry

    expect(providerFactoryCapabilities(factories)).toEqual([
      { connector_key: 'chat_sdk_v1', provider: 'discord' },
      { connector_key: 'custom', provider: 'github' },
    ])
  })

  it('rejects invalid, reserved, and duplicate provider registrations', () => {
    const factory = (connectorKey: string, provider: string): ProviderFactory => ({
      connectorKey,
      create: vi.fn(),
      provider,
    })

    expect(() => createProviderFactoryRegistry([factory('Chat SDK', 'discord')])).toThrow(
      'lowercase registry names',
    )
    expect(() => createProviderFactoryRegistry([factory('native_slack_v1', 'slack')])).toThrow(
      'cannot be delegated',
    )
    expect(() =>
      createProviderFactoryRegistry([
        factory('chat_sdk_v1', 'discord'),
        factory('chat_sdk_v1', 'discord'),
      ]),
    ).toThrow('duplicate channel provider factory')
  })

  it('closes Redis when a later gateway startup step fails', async () => {
    const redis = {
      ...testRedisClient(),
      close: vi.fn(() => Promise.resolve()),
      connect: vi.fn(() => Promise.resolve()),
      destroy: vi.fn(),
      onError: vi.fn(),
      ready: vi.fn(() => Promise.resolve(true)),
    }
    const listen = vi
      .spyOn(GatewayServer.prototype, 'listen')
      .mockRejectedValue(new Error('listener initialization failed'))
    const close = vi.spyOn(GatewayServer.prototype, 'close').mockResolvedValue()

    try {
      await expect(
        runGateway({
          createRedisClient: () => redis,
          config: loadConfig({
            OMNARA_CHANNEL_CORE_API_URL: 'http://api:8080/api/v1',
            OMNARA_CHANNEL_CONNECTOR_TOKEN: 'test-token',
            OMNARA_CHANNEL_GATEWAY_PUBLIC_URL: 'https://channels.example.test',
            OMNARA_CHANNEL_REDIS_TOPOLOGY: 'standalone',
            OMNARA_CHANNEL_REDIS_URL: 'redis://redis:6379/0',
          }),
          factories: [],
          logger: noopLogger,
        }),
      ).rejects.toThrow('listener initialization failed')
    } finally {
      listen.mockRestore()
      close.mockRestore()
    }

    expect(redis.close).toHaveBeenCalledOnce()
  })

  it('destroys Redis when shutdown interrupts a connection that never settles', async () => {
    const redis = {
      ...testRedisClient(),
      close: vi.fn(() => Promise.resolve()),
      connect: vi.fn(() => new Promise<void>(() => undefined)),
      destroy: vi.fn(),
      onError: vi.fn(),
      ready: vi.fn(() => Promise.resolve(false)),
    }
    const controller = new AbortController()
    const running = runGateway({
      createRedisClient: () => redis,
      config: loadConfig({
        OMNARA_CHANNEL_CORE_API_URL: 'http://api:8080/api/v1',
        OMNARA_CHANNEL_CONNECTOR_TOKEN: 'test-token',
        OMNARA_CHANNEL_GATEWAY_PUBLIC_URL: 'https://channels.example.test',
        OMNARA_CHANNEL_REDIS_TOPOLOGY: 'standalone',
        OMNARA_CHANNEL_STARTUP_TIMEOUT_MS: '300000',
        OMNARA_CHANNEL_REDIS_URL: 'redis://redis:6379/0',
      }),
      factories: [],
      logger: noopLogger,
      signal: controller.signal,
    })
    await vi.waitFor(() => {
      expect(redis.connect).toHaveBeenCalledOnce()
    })

    controller.abort(new Error('test shutdown'))

    await expect(running).rejects.toThrow('test shutdown')
    expect(redis.destroy).toHaveBeenCalledOnce()
    expect(redis.close).not.toHaveBeenCalled()
  })
})

const noopLogger: GatewayLogger = {
  debug: () => undefined,
  error: () => undefined,
  info: () => undefined,
  warn: () => undefined,
}

function testRedisClient() {
  return {
    close: vi.fn<GatewayRedisClient['close']>(unexpectedTestCall),
    connect: vi.fn<GatewayRedisClient['connect']>(unexpectedTestCall),
    destroy: vi.fn<GatewayRedisClient['destroy']>(unexpectedTestCall),
    onError: vi.fn<GatewayRedisClient['onError']>(),
    ready: vi.fn<GatewayRedisClient['ready']>(unexpectedTestCall),
    del: vi.fn<RedisStateClient['del']>(unexpectedTestCall),
    eval: vi.fn<RedisStateClient['eval']>(unexpectedTestCall),
    exists: vi.fn<RedisStateClient['exists']>(unexpectedTestCall),
    get: vi.fn<RedisStateClient['get']>(unexpectedTestCall),
    lLen: vi.fn<RedisStateClient['lLen']>(unexpectedTestCall),
    lPop: vi.fn<RedisStateClient['lPop']>(unexpectedTestCall),
    lRange: vi.fn<RedisStateClient['lRange']>(unexpectedTestCall),
    sAdd: vi.fn<RedisStateClient['sAdd']>(unexpectedTestCall),
    sIsMember: vi.fn<RedisStateClient['sIsMember']>(unexpectedTestCall),
    sRem: vi.fn<RedisStateClient['sRem']>(unexpectedTestCall),
    set: vi.fn<RedisStateClient['set']>(unexpectedTestCall),
    unlink: vi.fn<RedisStateClient['unlink']>(unexpectedTestCall),
  } satisfies GatewayRedisClient
}
