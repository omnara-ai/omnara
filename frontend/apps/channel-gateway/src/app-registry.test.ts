import {
  ApiError,
  type ChannelConnectorRuntimeUnit,
  type ChannelInboundEventRequest,
} from '@omnara/sdk'
import type { StateAdapter } from 'chat'
import { afterEach, describe, expect, it, type Mock, vi } from 'vitest'

import { AppRuntimeRegistry } from './app-registry'
import type { AppStateFactory } from './app-state'
import type { CoreClient } from './core-client'
import {
  type GatewayAppConfiguration,
  type GatewayLogger,
  type ProviderFactory,
  type ProviderFactoryContext,
  providerFactoryKey,
  type ProviderRuntime,
} from './types'

describe('channel app runtime registry', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('coalesces concurrent app loads and closes a cached runtime once', async () => {
    const runtime = testRuntime()
    const create = vi.fn(() => Promise.resolve(runtime))
    const factory = testFactory(create)
    const client = testClient(() => Promise.resolve(testConfiguration(1)))
    const registry = testRegistry(client, factory)

    const [first, second] = await Promise.all([
      registry.acquire(testConfiguration(1).app.id),
      registry.acquire(testConfiguration(1).app.id),
    ])

    expect(client.getAppConfiguration).toHaveBeenCalledOnce()
    expect(create).toHaveBeenCalledOnce()
    await first.release()
    await second.release()
    await registry.close()
    expect(runtime.close).toHaveBeenCalledOnce()
  })

  it('retires a changed app configuration without closing in-flight work', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-30T00:00:00Z'))
    let revision = 1
    const firstRuntime = testRuntime()
    const secondRuntime = testRuntime()
    const runtimes = [firstRuntime, secondRuntime]
    const lifecycleSignals: AbortSignal[] = []
    const create = vi.fn<ProviderFactory['create']>((context) => {
      lifecycleSignals.push(context.signal)
      const runtime = runtimes.shift()
      if (!runtime) throw new Error('unexpected provider runtime creation')
      return Promise.resolve(runtime)
    })
    const factory = testFactory(create)
    const client = testClient(() => Promise.resolve(testConfiguration(revision)))
    const registry = testRegistry(client, factory, 10)

    const first = await registry.acquire(testConfiguration(1).app.id)
    revision = 2
    vi.advanceTimersByTime(11)
    const second = await registry.acquire(testConfiguration(1).app.id)

    expect(create).toHaveBeenCalledTimes(2)
    expect(firstRuntime.close).not.toHaveBeenCalled()
    expect(lifecycleSignals[0]?.aborted).toBe(false)
    await first.release()
    expect(firstRuntime.close).toHaveBeenCalledOnce()
    expect(lifecycleSignals[0]?.aborted).toBe(true)
    await second.release()
    await registry.close()
    expect(secondRuntime.close).toHaveBeenCalledOnce()
    expect(lifecycleSignals[1]?.aborted).toBe(true)
  })

  it('fences an app load that finishes during shutdown', async () => {
    const configuration = deferred<GatewayAppConfiguration>()
    const runtime = testRuntime()
    const factory = testFactory(vi.fn(() => Promise.resolve(runtime)))
    const client = testClient(() => configuration.promise)
    const registry = testRegistry(client, factory)

    const acquire = registry.acquire(testConfiguration(1).app.id)
    await vi.waitFor(() => {
      expect(client.getAppConfiguration).toHaveBeenCalledOnce()
    })
    const closing = registry.close()
    configuration.resolve(testConfiguration(1))

    await expect(acquire).rejects.toThrow('channel app registry is closed')
    await closing
    expect(runtime.close).toHaveBeenCalledOnce()
  })

  it('fails closed when an expired configuration cannot be refreshed', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-30T00:00:00Z'))
    const getConfiguration = vi
      .fn<() => Promise<GatewayAppConfiguration>>()
      .mockResolvedValueOnce(testConfiguration(1))
      .mockRejectedValueOnce(new Error('core API unavailable'))
    const registry = testRegistry(
      testClient(getConfiguration),
      testFactory(() => Promise.resolve(testRuntime())),
      10,
    )

    const first = await registry.acquire(testConfiguration(1).app.id)
    await first.release()
    vi.advanceTimersByTime(11)

    await expect(registry.acquire(testConfiguration(1).app.id)).rejects.toThrow(
      'core API unavailable',
    )
    await registry.close()
  })

  it('retires a cached runtime when its app is definitively removed', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-30T00:00:00Z'))
    const runtime = testRuntime()
    const getConfiguration = vi
      .fn<() => Promise<GatewayAppConfiguration>>()
      .mockResolvedValueOnce(testConfiguration(1))
      .mockRejectedValueOnce(new ApiError(404, 'not found'))
    const state = testStateFactory()
    const registry = testRegistry(
      testClient(getConfiguration),
      testFactory(() => Promise.resolve(runtime)),
      10,
      1_000,
      10,
      state,
    )

    const first = await registry.acquire(testConfiguration(1).app.id)
    await first.release()
    vi.advanceTimersByTime(11)
    await expect(registry.acquire(testConfiguration(1).app.id)).rejects.toThrow('not found')
    expect(runtime.close).toHaveBeenCalledOnce()
    expect(state.clearSubscriptions).toHaveBeenCalledWith(testConfiguration(1).app.id)
    await registry.close()
  })

  it('clears deleted app subscriptions only after active handles release the runtime', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-30T00:00:00Z'))
    const runtime = testRuntime()
    const getConfiguration = vi
      .fn<() => Promise<GatewayAppConfiguration>>()
      .mockResolvedValueOnce(testConfiguration(1))
      .mockRejectedValueOnce(new ApiError(404, 'not found'))
    const state = testStateFactory()
    const registry = testRegistry(
      testClient(getConfiguration),
      testFactory(() => Promise.resolve(runtime)),
      10,
      1_000,
      10,
      state,
    )

    const active = await registry.acquire(testConfiguration(1).app.id)
    vi.advanceTimersByTime(11)
    await expect(registry.acquire(testConfiguration(1).app.id)).rejects.toThrow('not found')
    expect(runtime.close).not.toHaveBeenCalled()
    expect(state.clearSubscriptions).not.toHaveBeenCalled()

    await active.release()
    expect(runtime.close).toHaveBeenCalledOnce()
    expect(state.clearSubscriptions).toHaveBeenCalledWith(testConfiguration(1).app.id)
    await registry.close()
  })

  it('does not mask a definitive app removal when Redis cleanup fails', async () => {
    const state = testStateFactory()
    state.clearSubscriptions.mockRejectedValueOnce(new Error('Redis cleanup unavailable'))
    const registry = testRegistry(
      testClient(() => Promise.reject(new ApiError(404, 'not found'))),
      testFactory(() => Promise.resolve(testRuntime())),
      60_000,
      1_000,
      10,
      state,
    )

    await expect(registry.acquire(testConfiguration(1).app.id)).rejects.toThrow('not found')
    expect(state.clearSubscriptions).toHaveBeenCalledOnce()
    await registry.close()
  })

  it('retires a stale runtime when a newer configuration cannot start', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-30T00:00:00Z'))
    let revision = 1
    const runtime = testRuntime()
    const create = vi
      .fn<ProviderFactory['create']>()
      .mockResolvedValueOnce(runtime)
      .mockRejectedValueOnce(new Error('replacement adapter is invalid'))
    const registry = testRegistry(
      testClient(() => Promise.resolve(testConfiguration(revision))),
      testFactory(create),
      10,
    )

    const first = await registry.acquire(testConfiguration(1).app.id)
    await first.release()
    revision = 2
    vi.advanceTimersByTime(11)
    await expect(registry.acquire(testConfiguration(1).app.id)).rejects.toThrow(
      'replacement adapter is invalid',
    )
    expect(runtime.close).toHaveBeenCalledOnce()
    await registry.close()
  })

  it('injects inbound submission only into the active webhook context', async () => {
    const coreSubmit = vi.fn(() => Promise.resolve())
    const client = {
      getAppConfiguration: vi.fn(() => Promise.resolve(testConfiguration(1))),
      submitInbound: coreSubmit,
    } as unknown as CoreClient
    const runtime = testRuntime()
    runtime.handleWebhook = async (_request, context) => {
      await context.submitInbound(testInboundEvent)
      return new Response('accepted')
    }
    const factory = testFactory(() => Promise.resolve(runtime))
    const registry = testRegistry(client, factory)
    const handle = await registry.acquire(testConfiguration(1).app.id)

    await handle.handleWebhook(new Request('https://example.test/webhook'), {
      reserveWorkBytes: noopWorkReservation,
      waitUntil: () => undefined,
    })
    expect(coreSubmit).toHaveBeenCalledOnce()

    await handle.release()
    await registry.close()
  })

  it('keeps explicit inbound callbacks available to tracked webhook work', async () => {
    const tasks: Promise<unknown>[] = []
    const coreSubmit = vi.fn(() => Promise.resolve())
    const client = {
      getAppConfiguration: vi.fn(() => Promise.resolve(testConfiguration(1))),
      submitInbound: coreSubmit,
    } as unknown as CoreClient
    const runtime = testRuntime()
    runtime.handleWebhook = (_request, context) => {
      context.waitUntil(Promise.resolve().then(() => context.submitInbound(testInboundEvent)))
      return Promise.resolve(new Response('accepted'))
    }
    const factory = testFactory(() => Promise.resolve(runtime))
    const registry = testRegistry(client, factory)
    const handle = await registry.acquire(testConfiguration(1).app.id)

    const request = new Request('https://example.test/webhook')
    await handle.handleWebhook(request, {
      reserveWorkBytes: noopWorkReservation,
      waitUntil: (task) => tasks.push(task),
    })
    await Promise.all(tasks)
    expect(coreSubmit).toHaveBeenCalledWith(
      testConfiguration(1).app.id,
      testInboundEvent,
      request.signal,
    )

    await handle.release()
    await registry.close()
  })

  it('does not expose inbound authority through the long-lived factory context', async () => {
    let factoryContext: ProviderFactoryContext | undefined
    const coreSubmit = vi.fn(() => Promise.resolve())
    const client = {
      getAppConfiguration: vi.fn(() => Promise.resolve(testConfiguration(1))),
      submitInbound: coreSubmit,
    } as unknown as CoreClient
    const runtime = testRuntime()
    const factory = testFactory((context) => {
      factoryContext = context
      return Promise.resolve(runtime)
    })
    const registry = testRegistry(client, factory)
    const handle = await registry.acquire(testConfiguration(1).app.id)
    const webhook = handle.handleWebhook(new Request('https://example.test/webhook'), {
      reserveWorkBytes: noopWorkReservation,
      waitUntil: () => undefined,
    })

    await webhook
    expect(factoryContext).toBeDefined()
    expect(factoryContext).not.toHaveProperty('submitInbound')
    expect(factoryContext).not.toHaveProperty('resolveInteraction')
    expect(coreSubmit).not.toHaveBeenCalled()
    await handle.release()
    await registry.close()
  })

  it('binds runtime inbound authority to the exact leased unit', async () => {
    const submitRuntimeInbound = vi.fn(() => Promise.resolve())
    const client = {
      getAppConfiguration: vi.fn(() => Promise.resolve(testConfiguration(1))),
      submitRuntimeInbound,
    } as unknown as CoreClient
    const runtime = testRuntime()
    runtime.runUnit = async (_unit, context) => {
      await context.submitInbound(testInboundEvent)
    }
    const registry = testRegistry(
      client,
      testFactory(() => Promise.resolve(runtime)),
    )
    const handle = await registry.acquire(testConfiguration(1).app.id)
    const unit = testRuntimeUnit()
    const signal = new AbortController().signal

    await handle.runUnit(unit, {
      reserveWorkBytes: noopWorkReservation,
      signal,
      updateCheckpoint: () => undefined,
    })

    expect(submitRuntimeInbound).toHaveBeenCalledWith(
      testConfiguration(1).app.id,
      unit,
      testInboundEvent,
      signal,
    )
    await handle.release()
    await registry.close()
  })

  it('aborts provider creation at its lifecycle deadline and closes a late runtime', async () => {
    vi.useFakeTimers()
    const pending = deferred<ProviderRuntime>()
    const runtime = testRuntime()
    let creationSignal: AbortSignal | undefined
    const factory = testFactory((context) => {
      creationSignal = context.signal
      return pending.promise
    })
    const registry = testRegistry(
      testClient(() => Promise.resolve(testConfiguration(1))),
      factory,
      60_000,
      1_000,
    )

    const acquire = registry.acquire(testConfiguration(1).app.id)
    const rejected = expect(acquire).rejects.toThrow(
      'provider runtime creation reached its deadline',
    )
    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(1_001)
    await rejected
    expect(creationSignal?.aborted).toBe(true)

    pending.resolve(runtime)
    await vi.advanceTimersByTimeAsync(0)
    expect(runtime.close).toHaveBeenCalledOnce()
    await registry.close()
  })

  it('clears the lifecycle timer when a provider factory throws synchronously', async () => {
    vi.useFakeTimers()
    const factory = testFactory(() => {
      throw new Error('invalid provider configuration')
    })
    const registry = testRegistry(
      testClient(() => Promise.resolve(testConfiguration(1))),
      factory,
    )

    await expect(registry.acquire(testConfiguration(1).app.id)).rejects.toThrow(
      'invalid provider configuration',
    )
    expect(vi.getTimerCount()).toBe(0)
    await registry.close()
  })

  it('clears the lifecycle timer after a provider runtime closes', async () => {
    vi.useFakeTimers()
    const runtime = testRuntime()
    const registry = testRegistry(
      testClient(() => Promise.resolve(testConfiguration(1))),
      testFactory(() => Promise.resolve(runtime)),
    )
    const handle = await registry.acquire(testConfiguration(1).app.id)
    await handle.release()

    await registry.close()

    expect(runtime.close).toHaveBeenCalledOnce()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('aborts a provider lifecycle before closing its runtime', async () => {
    let lifecycleSignal: AbortSignal | undefined
    const runtime = testRuntime()
    runtime.close.mockImplementation(() => {
      expect(lifecycleSignal?.aborted).toBe(true)
      return Promise.resolve()
    })
    const registry = testRegistry(
      testClient(() => Promise.resolve(testConfiguration(1))),
      testFactory((context) => {
        lifecycleSignal = context.signal
        return Promise.resolve(runtime)
      }),
    )
    const handle = await registry.acquire(testConfiguration(1).app.id)
    await handle.release()

    await registry.close()

    expect(lifecycleSignal?.aborted).toBe(true)
    expect(runtime.close).toHaveBeenCalledOnce()
  })

  it('evicts the least recently used idle app in constant-time cache order', async () => {
    const firstAppId = 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa'
    const secondAppId = 'iapp_bbbbbbbbbbbbbbbbbbbbbbbbbb'
    const thirdAppId = 'iapp_cccccccccccccccccccccccccc'
    const appIds = [firstAppId, secondAppId, thirdAppId]
    const configurations = new Map(appIds.map((appId) => [appId, testConfiguration(1, appId)]))
    const runtimes = new Map(appIds.map((appId) => [appId, testRuntime()]))
    const client = testClient((appId) => {
      const configuration = configurations.get(appId)
      if (!configuration) return Promise.reject(new Error(`unexpected app ${appId}`))
      return Promise.resolve(configuration)
    })
    const factory = testFactory((context) => {
      const runtime = runtimes.get(context.configuration.app.id)
      if (!runtime) return Promise.reject(new Error('unexpected provider runtime'))
      return Promise.resolve(runtime)
    })
    const registry = testRegistry(client, factory, 60_000, 1_000, 2)
    const acquireAndRelease = async (appId: string) => {
      const handle = await registry.acquire(appId)
      await handle.release()
    }

    await acquireAndRelease(firstAppId)
    await acquireAndRelease(secondAppId)
    await acquireAndRelease(firstAppId)
    await acquireAndRelease(thirdAppId)

    expect(runtimes.get(firstAppId)?.close).not.toHaveBeenCalled()
    expect(runtimes.get(secondAppId)?.close).toHaveBeenCalledOnce()
    expect(runtimes.get(thirdAppId)?.close).not.toHaveBeenCalled()
    await registry.close()
  })

  it('keeps a healthy cached runtime when a replacement candidate fails', async () => {
    const healthyAppId = 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa'
    const invalidAppId = 'iapp_bbbbbbbbbbbbbbbbbbbbbbbbbb'
    const healthyRuntime = testRuntime()
    const client = testClient((appId) => {
      if (appId === healthyAppId) return Promise.resolve(testConfiguration(1, healthyAppId))
      return Promise.reject(new Error('candidate configuration is unavailable'))
    })
    const registry = testRegistry(
      client,
      testFactory(() => Promise.resolve(healthyRuntime)),
      60_000,
      1_000,
      1,
    )

    const first = await registry.acquire(healthyAppId)
    await first.release()
    await expect(registry.acquire(invalidAppId)).rejects.toThrow(
      'candidate configuration is unavailable',
    )
    expect(healthyRuntime.close).not.toHaveBeenCalled()

    const retained = await registry.acquire(healthyAppId)
    await retained.release()
    expect(client.getAppConfiguration).toHaveBeenCalledTimes(2)
    await registry.close()
    expect(healthyRuntime.close).toHaveBeenCalledOnce()
  })
})

function noopWorkReservation() {
  return { release: () => undefined, resize: () => undefined }
}

function testRegistry(
  client: CoreClient,
  factory: ProviderFactory,
  refreshAfterMs = 60_000,
  providerLifecycleTimeoutMs = 1_000,
  maxApps = 10,
  state = testStateFactory(),
): AppRuntimeRegistry {
  return new AppRuntimeRegistry({
    client,
    factories: new Map([[providerFactoryKey(factory.connectorKey, factory.provider), factory]]),
    logger: noopLogger,
    maxApps,
    maxConcurrentLoads: 2,
    maxInstallations: 10,
    notFoundCacheMs: 1_000,
    providerLifecycleTimeoutMs,
    reserveWorkBytes: noopWorkReservation,
    refreshAfterMs,
    state,
  })
}

function testStateFactory(): AppStateFactory & {
  clearSubscriptions: ReturnType<typeof vi.fn>
  markKnownApp: ReturnType<typeof vi.fn>
} {
  return {
    clearSubscriptions: vi.fn(() => Promise.resolve()),
    forApp: () => ({}) as StateAdapter,
    markKnownApp: vi.fn(() => Promise.resolve()),
  }
}

function testClient(
  getConfiguration: (integrationAppId: string) => Promise<GatewayAppConfiguration>,
): CoreClient & { getAppConfiguration: ReturnType<typeof vi.fn> } {
  return {
    getAppConfiguration: vi.fn(getConfiguration),
  } as unknown as CoreClient & { getAppConfiguration: ReturnType<typeof vi.fn> }
}

function testFactory(create: ProviderFactory['create']): ProviderFactory {
  return { connectorKey: 'chat_sdk_v1', create, provider: 'discord' }
}

function testRuntime(): ProviderRuntime & { close: Mock<() => Promise<void>> } {
  return {
    close: vi.fn(() => Promise.resolve()),
    handleWebhook: () => Promise.resolve(new Response()),
    send: () => Promise.resolve({ providerMessageRef: '' }),
  }
}

function testConfiguration(
  revision: number,
  appId = 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
): GatewayAppConfiguration {
  return {
    app: {
      configuration_revision: revision,
      connector_key: 'chat_sdk_v1',
      display_name: 'Discord test app',
      id: appId,
      provider: 'discord',
      provider_app_ref: 'discord-app-1',
      provider_config: { revision },
      provider_metadata: {},
      updated_at: new Date(revision * 1_000).toISOString(),
    },
  }
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((settle) => {
    resolve = settle
  })
  return { promise, resolve }
}

const noopLogger: GatewayLogger = {
  debug: () => undefined,
  error: () => undefined,
  info: () => undefined,
  warn: () => undefined,
}

const testInboundEvent: ChannelInboundEventRequest = {
  actor: { display_name: 'Alice', metadata: {}, ref: 'user-1' },
  content_blocks: [{ text: 'hello', type: 'text' }],
  conversation: {
    direct: false,
    kind: 'thread',
    mentioned: true,
    metadata: {},
    ref: 'thread-1',
  },
  event_type: 'message',
  external_account_ref: 'account-1',
  external_tenant_id: 'tenant-1',
  metadata: {},
  occurred_at: '2026-08-30T00:00:00Z',
  provider_event_id: 'event-1',
  version: 'v1',
}

function testRuntimeUnit(): ChannelConnectorRuntimeUnit {
  return {
    checkpoint: {},
    checkpoint_revision: 0,
    checkpoint_version: 1,
    configuration: {},
    created_at: '2026-08-30T00:00:00Z',
    desired_state: 'running',
    id: 'irun_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    last_error: {},
    lease_app_configuration_revision: 1,
    lease_generation: 1,
    lease_spec_revision: 1,
    lease_token: '00000000-0000-7000-8000-000000000001',
    runtime_kind: 'provider_socket',
    spec_revision: 1,
    status: 'running',
    unit_key: 'shard-0',
    updated_at: '2026-08-30T00:00:00Z',
  }
}
