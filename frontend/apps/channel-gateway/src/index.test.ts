import {
  ApiError,
  type ChannelConnectorControlReceipt,
  type ChannelConnectorEventReceipt,
} from '@omnara/sdk'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { loadConfig } from './config'
import { CoreClient } from './core/client'
import { initialReceiptWorkBytes } from './core/receipt-http'
import * as discordGateway from './discord/gateway'
import { testRuntimeHandle, unexpectedTestCall } from './gateway-test-fixtures'
import * as githubGateway from './github/gateway'
import { GatewayServer } from './http/server'
import {
  createProviderFactoryRegistry,
  providerFactoryCapabilities,
  runGateway,
  type RunGatewayOptions,
} from './index'
import { type OperationsOptions, operationsRoute } from './operations/handler'
import type { RedisStateClient } from './redis-client'
import type { GatewayRedisClient } from './redis-client'
import { AppRuntimeRegistry } from './runtime/registry'
import * as slackGateway from './slack/gateway'
import {
  type ControlReceiptBehavior,
  type GatewayLogger,
  type ProviderFactory,
  type ProviderFactoryRegistry,
  type ReceiptBehavior,
  ReceiptBehaviorError,
} from './types'
import { GatewayAtCapacityError } from './work-budget'

describe('channel gateway provider capabilities', () => {
  beforeEach(() => {
    vi.spyOn(CoreClient.prototype, 'claimNextControlEvent').mockResolvedValue(undefined)
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })
  it('boots all built-in providers and dispatches their operations through the authenticated HTTP boundary', async () => {
    const controller = new AbortController()
    const redis = {
      ...testRedisClient(),
      connect: vi.fn().mockResolvedValue(undefined),
      close: vi.fn().mockResolvedValue(undefined),
      ready: vi.fn().mockResolvedValue(true),
    }
    const execute = () =>
      vi.fn<OperationsOptions['execute']>().mockResolvedValue({
        outcome: 'completed',
        payload: { publication: 'published' },
      })
    const executions = { slack: execute(), discord: execute(), github: execute() }
    const spies = [
      vi.spyOn(slackGateway, 'createSlackGateway').mockReturnValue({
        executeOperation: executions.slack,
        processReceipt: vi.fn().mockResolvedValue(undefined),
      }),
      vi.spyOn(discordGateway, 'createDiscordGateway').mockReturnValue({
        executeOperation: executions.discord,
      }),
      vi.spyOn(githubGateway, 'createGitHubGateway').mockReturnValue({
        executeOperation: executions.github,
      }),
    ]
    const claims = vi.spyOn(CoreClient.prototype, 'claimNextEvent').mockResolvedValue(undefined)
    const runtimes = vi.spyOn(CoreClient.prototype, 'claimRuntimeUnits').mockResolvedValue([])
    const listen = vi.spyOn(GatewayServer.prototype, 'listen')
    const config = { ...testConfig(), port: 0, idlePollMs: 10 }
    const running = runGateway({
      config,
      createRedisClient: () => redis,
      logger: noopLogger,
      signal: controller.signal,
    })
    try {
      await vi.waitFor(() => {
        expect(listen).toHaveBeenCalledOnce()
        expect(new Set(claims.mock.calls.map(([capability]) => capability.provider))).toEqual(
          new Set(['slack', 'discord', 'github']),
        )
        expect(new Set(runtimes.mock.calls.map(([capability]) => capability.provider))).toEqual(
          new Set(['slack', 'discord', 'github']),
        )
      })
      const listening = listen.mock.results[0]
      if (listening?.type !== 'return') throw new Error('gateway did not listen')
      const port = await listening.value
      for (const [provider, execution] of Object.entries(executions)) {
        const body = JSON.stringify({
          request_id: `default-${provider}`,
          capability: { connector_key: 'omnara', provider },
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
        })
        const url = `http://127.0.0.1:${port}${operationsRoute}`
        const denied = await fetch(url, {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body,
        })
        expect(denied.status).toBe(401)
        await denied.text()
        expect(execution).not.toHaveBeenCalled()
        const response = await fetch(url, {
          method: 'POST',
          headers: {
            authorization: `Bearer ${config.connectorToken}`,
            'content-type': 'application/json',
          },
          body,
        })
        expect(response.status).toBe(200)
        expect(await response.json()).toMatchObject({
          request_id: `default-${provider}`,
          outcome: 'completed',
        })
        expect(execution).toHaveBeenCalledOnce()
        expect(execution.mock.calls[0]?.[0].capability).toEqual({
          connector_key: 'omnara',
          provider,
        })
      }
    } finally {
      controller.abort()
      await running
      for (const spy of [...spies, claims, runtimes, listen]) spy.mockRestore()
    }
    expect(redis.close).toHaveBeenCalledOnce()
  })

  it.each([
    { cause: undefined, state: 'completed' },
    { cause: new ApiError(503, 'temporarily unavailable'), state: 'pending' },
    { cause: new GatewayAtCapacityError('channel app registry is at capacity'), state: 'pending' },
    { cause: new ApiError(404, 'cached missing app'), state: 'pending' },
    { cause: new Error('channel app registry is closed'), state: 'pending' },
    { cause: new Error('factory initialization failed'), state: 'pending' },
    { cause: new Error('factory initialization failed'), state: 'failed', attemptCount: 8 },
    { cause: new ReceiptBehaviorError(false), state: 'failed' },
  ])(
    'dispatches receipts through their app and records $state after registry resolution',
    async ({ cause, state, attemptCount = 1 }) => {
      const controller = new AbortController()
      const redis = {
        ...testRedisClient(),
        connect: vi.fn().mockResolvedValue(undefined),
        close: vi.fn().mockResolvedValue(undefined),
      }
      const receipt: ChannelConnectorEventReceipt = {
        receipt_id: `irec_${'a'.repeat(26)}`,
        integration_app_id: `iapp_${'a'.repeat(26)}`,
        integration_install_id: `iin_${'a'.repeat(26)}`,
        event_id: 'verified-provider-event',
        state: 'processing',
        attempt_count: attemptCount,
        lease_token: '01994550-1234-7123-8123-123456789abc',
        lease_generation: 1,
        lease_expires_at: new Date(Date.now() + 60_000).toISOString(),
        last_error: {},
        payload: {},
      }
      const behavior = vi.fn<ReceiptBehavior>().mockResolvedValue(undefined)
      const handle = testRuntimeHandle({
        runtime: {
          close: () => Promise.resolve(),
          handleWebhook: unexpectedTestCall,
          processReceipt: behavior,
        },
      })
      const acquire = vi.spyOn(AppRuntimeRegistry.prototype, 'acquire')
      if (cause) acquire.mockRejectedValue(cause)
      else acquire.mockResolvedValue(handle)
      const claims = vi
        .spyOn(CoreClient.prototype, 'claimNextEvent')
        .mockResolvedValue(undefined)
        .mockResolvedValueOnce(receipt)
      const runtimes = vi.spyOn(CoreClient.prototype, 'claimRuntimeUnits').mockResolvedValue([])
      const complete = vi.spyOn(CoreClient.prototype, 'completeEvent').mockResolvedValue(undefined)
      const running = runGateway({
        config: { ...testConfig(), port: 0 },
        createRedisClient: () => redis,
        logger: noopLogger,
        signal: controller.signal,
      })
      try {
        await vi.waitFor(() => {
          expect(complete).toHaveBeenCalledOnce()
        })
        expect(acquire).toHaveBeenCalledWith(receipt.integration_app_id)
        expect(complete.mock.calls[0]?.[1]).toMatchObject({ state })
        if (cause) expect(behavior).not.toHaveBeenCalled()
        else {
          expect(behavior).toHaveBeenCalledOnce()
          expect(behavior.mock.calls[0]?.[0]).toEqual(receipt)
          expect(behavior.mock.calls[0]?.[1].signal).toBeInstanceOf(AbortSignal)
          expect(handle.release).toHaveBeenCalledOnce()
        }
      } finally {
        controller.abort()
        await running
        for (const spy of [acquire, claims, runtimes, complete]) spy.mockRestore()
      }
    },
  )

  it.each([
    { cause: undefined, outcome: 'yield', phase: 'acquire' },
    { cause: new ApiError(503, 'temporarily unavailable'), outcome: 'retry', phase: 'acquire' },
    { cause: new GatewayAtCapacityError(), outcome: 'retry', phase: 'acquire' },
    { cause: new ApiError(404, 'cached missing app'), outcome: 'retry', phase: 'acquire' },
    { cause: new ApiError(404, 'removed child'), outcome: 'retry', phase: 'behavior' },
  ])(
    'runs provider control work independently and reports $outcome after $phase',
    async ({ cause, outcome, phase }) => {
      const controller = new AbortController()
      const redis = {
        ...testRedisClient(),
        connect: vi.fn().mockResolvedValue(undefined),
        close: vi.fn().mockResolvedValue(undefined),
      }
      const receipt: ChannelConnectorControlReceipt = {
        receipt_id: `icrc_${'a'.repeat(26)}`,
        integration_app_id: `iapp_${'a'.repeat(26)}`,
        provider_tenant_id: '42',
        event_id: 'verified-installation-restored',
        payload: {},
        state: 'processing',
        last_installation_id: null,
        end_installation_id: `iin_${'b'.repeat(26)}`,
        lease_token: '01994550-1234-7123-8123-123456789abc',
        lease_generation: 90,
        lease_expires_at: new Date(Date.now() + 60_000).toISOString(),
        attempts_since_progress: 30,
        last_error: {},
        created_at: new Date().toISOString(),
      }
      const progress = { outcome: 'yield', last_installation_id: `iin_${'a'.repeat(26)}` } as const
      const config = { ...testConfig(), port: 0, idlePollMs: 10 }
      const behavior = vi.fn<ControlReceiptBehavior>().mockImplementation((_receipt, context) => {
        // The real composition must cap controls even while the parent budget
        // has ample capacity. Claims already reserve their small envelope.
        const remaining = Math.floor(config.webhookMaxBufferedBytes / 2) - initialReceiptWorkBytes
        const work = context.reserveWorkBytes(remaining)
        try {
          expect(() => context.reserveWorkBytes(1)).toThrow(GatewayAtCapacityError)
        } finally {
          work.release()
        }
        return Promise.resolve(progress)
      })
      if (cause && phase === 'behavior') behavior.mockRejectedValue(cause)
      const handle = testRuntimeHandle({
        runtime: {
          close: () => Promise.resolve(),
          handleWebhook: unexpectedTestCall,
          processControlReceipt: behavior,
        },
      })
      const acquire = vi.spyOn(AppRuntimeRegistry.prototype, 'acquire')
      if (cause && phase === 'acquire') acquire.mockRejectedValue(cause)
      else acquire.mockResolvedValue(handle)
      vi.spyOn(CoreClient.prototype, 'claimNextEvent').mockResolvedValue(undefined)
      vi.spyOn(CoreClient.prototype, 'claimRuntimeUnits').mockResolvedValue([])
      let claimed = false
      vi.spyOn(CoreClient.prototype, 'claimNextControlEvent').mockImplementation((capability) => {
        if (capability.provider !== 'github' || claimed) return Promise.resolve(undefined)
        claimed = true
        return Promise.resolve(receipt)
      })
      const complete = vi
        .spyOn(CoreClient.prototype, 'completeControlEvent')
        .mockResolvedValue(undefined)
      const running = runGateway({
        config,
        createRedisClient: () => redis,
        logger: noopLogger,
        signal: controller.signal,
      })
      try {
        await vi.waitFor(() => {
          expect(complete).toHaveBeenCalledOnce()
        })
        expect(complete.mock.calls[0]?.[0]).toEqual(receipt)
        expect(complete.mock.calls[0]?.[1]).toMatchObject({ outcome })
        if (cause && phase === 'acquire') expect(behavior).not.toHaveBeenCalled()
        else {
          if (!cause) expect(complete.mock.calls[0]?.[1]).toEqual(progress)
          expect(behavior).toHaveBeenCalledOnce()
          expect(behavior.mock.calls[0]?.[1].reserveWorkBytes).toBeTypeOf('function')
          expect(handle.release).toHaveBeenCalledOnce()
        }
      } finally {
        controller.abort()
        await running
      }
    },
  )

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
        'test_connector/discord',
        {
          connectorKey: 'test_connector',
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
      { connector_key: 'test_connector', provider: 'discord' },
      { connector_key: 'custom', provider: 'github' },
    ])
  })

  it('rejects invalid and duplicate provider registrations', () => {
    const factory = (connectorKey: string, provider: string): ProviderFactory => ({
      connectorKey,
      create: vi.fn(),
      provider,
    })

    expect(() => createProviderFactoryRegistry([factory('Omnara', 'discord')])).toThrow(
      'lowercase registry names',
    )
    expect(() =>
      createProviderFactoryRegistry([
        factory('test_connector', 'discord'),
        factory('test_connector', 'discord'),
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
  return { connectorKey: 'test_connector', provider: 'fixture', create: vi.fn() }
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
    eval: vi.fn<RedisStateClient['eval']>(unexpectedTestCall),
    set: vi.fn<RedisStateClient['set']>(unexpectedTestCall),
  } satisfies GatewayRedisClient
}
