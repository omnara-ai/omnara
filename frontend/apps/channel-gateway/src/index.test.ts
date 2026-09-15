import { describe, expect, it, vi } from 'vitest'

import { AppRuntimeRegistry } from './app-registry'
import type { RedisStateClient } from './app-state'
import { loadConfig } from './config'
import { CoreClient } from './core-client'
import { unexpectedTestCall } from './gateway-test-fixtures'
import {
  createProviderFactoryRegistry,
  providerFactoryCapabilities,
  runGateway,
  type RunGatewayOptions,
} from './index'
import { type OperationsOptions, operationsRoute } from './operations'
import type { GatewayRedisClient } from './redis-client'
import { GatewayServer } from './server'
import type { GatewayLogger, ProviderFactory, ProviderFactoryRegistry } from './types'

describe('channel gateway provider capabilities', () => {
  it.each(['redisUrl', 'redisTopology'] as const)(
    'requires %s when a provider factory is enabled',
    async (field) => {
      const config = { ...testConfig(), [field]: undefined }
      const createRedisClient =
        vi.fn<NonNullable<RunGatewayOptions['createRedisClient']>>(unexpectedTestCall)
      const listen = vi.spyOn(GatewayServer.prototype, 'listen')
      try {
        await expect(
          runGateway({ config, createRedisClient, factories: [testFactory()], logger: noopLogger }),
        ).rejects.toThrow('required when provider factories are enabled')
        expect(createRedisClient).not.toHaveBeenCalled()
        expect(listen).not.toHaveBeenCalled()
      } finally {
        listen.mockRestore()
      }
    },
  )

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

  it('rejects invalid and duplicate provider registrations', () => {
    const factory = (connectorKey: string, provider: string): ProviderFactory => ({
      connectorKey,
      create: vi.fn(),
      provider,
    })

    expect(() => createProviderFactoryRegistry([factory('Chat SDK', 'discord')])).toThrow(
      'lowercase registry names',
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
          factories: [testFactory()],
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
      factories: [testFactory()],
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

  it.each([false, true])(
    'runs receipts and operations without Redis when factories are empty (Redis configured: %s)',
    async (redisConfigured) => {
      const controller = new AbortController()
      const redis = testRedisClient()
      const createRedisClient = vi.fn(() => redis)
      const listen = vi.spyOn(GatewayServer.prototype, 'listen')
      const acquire = vi.spyOn(AppRuntimeRegistry.prototype, 'acquire')
      const runtime = vi.spyOn(CoreClient.prototype, 'claimRuntimeUnits')
      let claimSignal: AbortSignal | undefined
      const claim = vi
        .spyOn(CoreClient.prototype, 'claimNextEvent')
        .mockImplementation((_capability, _leaseMs, signal, work) => {
          claimSignal = signal
          expect(work).toBeDefined()
          return new Promise((_resolve, reject) => {
            signal?.addEventListener(
              'abort',
              () => {
                reject(new Error('aborted'))
              },
              { once: true },
            )
          })
        })
      const behavior = vi.fn().mockResolvedValue(undefined)
      const config = { ...testConfig(redisConfigured), port: 0 }
      const execute = vi.fn<OperationsOptions['execute']>().mockResolvedValue({
        outcome: 'completed',
        payload: { publication: 'published' },
      })
      const running = runGateway({
        config,
        createRedisClient,
        factories: [],
        logger: noopLogger,
        receipts: receiptOptions(behavior),
        operations: {
          credential: config.connectorToken,
          allowedCapabilities: [{ connector_key: 'receipt_fixture', provider: 'fixture' }],
          execute,
          maxConcurrentRequests: config.operationMaxConcurrentRequests,
          maxTemporaryBytes: config.operationMaxTemporaryBytes,
          maxRequestBytes: config.operationMaxRequestBytes,
          maxDurationMs: config.operationMaxDurationMs,
          temporaryDirectory: config.operationTemporaryDirectory,
        },
        signal: controller.signal,
      })
      try {
        await vi.waitFor(() => {
          expect(claim).toHaveBeenCalledOnce()
        })
        expect(claim.mock.calls[0]?.slice(0, 2)).toEqual([
          { connector_key: 'receipt_fixture', provider: 'fixture' },
          1000,
        ])
        expect(runtime).not.toHaveBeenCalled()
        expect(behavior).not.toHaveBeenCalled()
        const listening = listen.mock.results[0]
        if (listening?.type !== 'return') throw new Error('gateway did not start listening')
        const port = await listening.value
        expect(port).toBeGreaterThan(0)
        const baseUrl = `http://127.0.0.1:${port}`
        const ready = await fetch(`${baseUrl}/readyz`)
        expect(ready.status).toBe(200)
        expect(await ready.json()).toEqual({ ok: true })
        const webhook = await fetch(`${baseUrl}/hooks/iapp_${'a'.repeat(26)}/fixture/events`, {
          method: 'POST',
          body: '{}',
        })
        expect(webhook.status).toBe(404)
        await webhook.text()
        const operation = await fetch(`${baseUrl}${operationsRoute}`, {
          method: 'POST',
          headers: {
            authorization: `Bearer ${config.connectorToken}`,
            'content-type': 'application/json',
          },
          body: JSON.stringify({
            request_id: 'without-redis',
            capability: { connector_key: 'receipt_fixture', provider: 'fixture' },
            kind: 'send',
            scope: {
              project_id: 'project',
              integration_app_id: 'app',
              integration_install_id: 'install',
              agent_id: 'agent',
              channel_id: 'channel',
            },
            deadline: new Date(Date.now() + 5000).toISOString(),
            payload: { message: { text: 'hello' } },
          }),
        })
        expect(operation.status).toBe(200)
        expect(await operation.json()).toEqual({
          request_id: 'without-redis',
          outcome: 'completed',
          payload: { publication: 'published' },
        })
        expect(execute).toHaveBeenCalledOnce()
        expect(acquire).not.toHaveBeenCalled()
        expect(createRedisClient).not.toHaveBeenCalled()
        expect(redis.connect).not.toHaveBeenCalled()
        expect(redis.ready).not.toHaveBeenCalled()
        controller.abort()
        await running
        expect(claimSignal?.aborted).toBe(true)
        expect(redis.close).not.toHaveBeenCalled()
        expect(redis.destroy).not.toHaveBeenCalled()
      } finally {
        controller.abort()
        await running
        for (const spy of [claim, runtime, listen, acquire]) spy.mockRestore()
      }
    },
  )

  it('requires valid receipt deployment budgets before opening the listener', async () => {
    const redis = {
      ...testRedisClient(),
      connect: vi.fn().mockResolvedValue(undefined),
      close: vi.fn().mockResolvedValue(undefined),
    }
    const listen = vi.spyOn(GatewayServer.prototype, 'listen').mockResolvedValue(0)
    try {
      await expect(
        runGateway({
          config: testConfig(),
          createRedisClient: () => redis,
          factories: [testFactory()],
          logger: noopLogger,
          receipts: { ...receiptOptions(vi.fn()), maxConcurrentEvents: 0 },
        }),
      ).rejects.toThrow('invalid channel receipt consumer configuration')
      expect(listen).not.toHaveBeenCalled()
      expect(redis.close).toHaveBeenCalledOnce()
    } finally {
      listen.mockRestore()
    }
  })
})

function testFactory(): ProviderFactory {
  return { connectorKey: 'chat_sdk_v1', provider: 'fixture', create: vi.fn() }
}

function testConfig(redisConfigured = true) {
  return loadConfig({
    OMNARA_CHANNEL_CORE_API_URL: 'http://api:8080/api/v1',
    OMNARA_CHANNEL_CONNECTOR_TOKEN: 'test-token',
    OMNARA_CHANNEL_GATEWAY_PUBLIC_URL: 'https://channels.example.test',
    OMNARA_CHANNEL_REDIS_TOPOLOGY: redisConfigured ? 'standalone' : undefined,
    OMNARA_CHANNEL_REDIS_URL: redisConfigured ? 'redis://redis:6379/0' : undefined,
  })
}

function receiptOptions(
  behavior: NonNullable<RunGatewayOptions['receipts']>['behavior'],
): NonNullable<RunGatewayOptions['receipts']> {
  return {
    capabilities: [{ connector_key: 'receipt_fixture', provider: 'fixture' }],
    behavior,
    maxConcurrentEvents: 1,
    maxAttempts: 3,
    leaseMs: 1000,
    claimTimeoutMs: 500,
    behaviorTimeoutMs: 200,
    completionTimeoutMs: 100,
    idlePollMs: 10,
  }
}

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
