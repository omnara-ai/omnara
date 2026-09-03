import type { ChannelConnectorDelivery, CompleteChannelConnectorDeliveryRequest } from '@omnara/sdk'
import { describe, expect, it, type Mock, vi } from 'vitest'

import type { AppRuntimeRegistry } from './app-registry'
import type { CoreClient } from './core-client'
import { DeliveryLoop } from './delivery-loop'
import { maxDiagnosticMessageBytes } from './diagnostics'
import { type GatewayLogger, ProviderDeliveryError, type ProviderRuntime } from './types'

describe('channel delivery loop', () => {
  it('claims exact capabilities in round-robin order', async () => {
    const controller = new AbortController()
    const capabilities = [
      { connector_key: 'chat_sdk_v1', provider: 'discord' },
      { connector_key: 'custom_v1', provider: 'github' },
    ]
    const claimed: typeof capabilities = []
    const client = {
      claimDeliveries: vi.fn((capability: (typeof capabilities)[number]) => {
        claimed.push(capability)
        if (claimed.length === 3) controller.abort(new Error('test complete'))
        return Promise.resolve([])
      }),
    } as unknown as CoreClient

    await new DeliveryLoop({
      capabilities,
      claimLimit: 10,
      client,
      completionTimeoutMs: 5_000,
      idlePollMs: 1,
      leaseMs: 30_000,
      logger: noopLogger,
      owner: 'gateway-test',
      random: () => 0.5,
      registry: {} as AppRuntimeRegistry,
      sendTimeoutMs: 20_000,
    }).run(controller.signal)

    expect(claimed).toEqual([capabilities[0], capabilities[1], capabilities[0]])
  })

  it('sends a claimed delivery and records the provider message reference', async () => {
    const send = vi.fn(() => Promise.resolve({ providerMessageRef: 'provider-message-1' }))
    const { completion, run } = deliveryJourney(send)

    await run()

    expect(send).toHaveBeenCalledOnce()
    expect(completion()).toMatchObject({
      claim_generation: 1,
      claim_token: '00000000-0000-7000-8000-000000000001',
      outcome: 'delivered',
      provider_message_ref: 'provider-message-1',
    })
  })

  it('uses a local lease deadline instead of trusting the core server clock', async () => {
    const send = vi.fn(() => Promise.resolve({ providerMessageRef: 'provider-message-1' }))
    const { completion, run } = deliveryJourney(
      send,
      undefined,
      20_000,
      undefined,
      1,
      undefined,
      '2000-01-01T00:00:00.000Z',
    )

    await run()

    expect(send).toHaveBeenCalledOnce()
    expect(completion()).toMatchObject({ outcome: 'delivered' })
  })

  it('records delivery success without an oversized provider message reference', async () => {
    const { completion, run } = deliveryJourney(() =>
      Promise.resolve({ providerMessageRef: '界'.repeat(683) }),
    )

    await run()

    expect(completion()).toMatchObject({ outcome: 'delivered', provider_message_ref: '' })
  })

  it('retries a provider-declared rate limit', async () => {
    const { completion, run } = deliveryJourney(() =>
      Promise.reject(
        new ProviderDeliveryError('rate limited before send', {
          retryAfterMs: 2_500,
          retryable: true,
        }),
      ),
    )

    await run()

    expect(completion()).toMatchObject({ outcome: 'retry_wait', retry_after_ms: 2_500 })
  })

  it.each([
    { attemptCount: 7, outcome: 'retry_wait' },
    { attemptCount: 8, outcome: 'failed' },
  ] as const)(
    'bounds definite provider retries at attempt $attemptCount',
    async ({ attemptCount, outcome }) => {
      const { completion, run } = deliveryJourney(
        () =>
          Promise.reject(
            new ProviderDeliveryError('provider remained unavailable', { retryable: true }),
          ),
        undefined,
        20_000,
        undefined,
        attemptCount,
      )

      await run()

      expect(completion()).toMatchObject({ outcome })
    },
  )

  it('treats installation preparation failures as definite and retryable', async () => {
    const send = vi.fn(() => Promise.resolve({ providerMessageRef: 'unexpected' }))
    const { completion, run } = deliveryJourney(
      send,
      () => Promise.reject(new Error('core configuration temporarily unavailable')),
      20_000,
      () => 0,
    )

    await run()

    expect(send).not.toHaveBeenCalled()
    expect(completion()).toMatchObject({
      outcome: 'retry_wait',
      last_error: { code: 'transient_failure' },
      retry_after_ms: 250,
    })
  })

  it('adds equal jitter to generic retries without shortening provider Retry-After', async () => {
    const generic = deliveryJourney(
      () => Promise.resolve({ providerMessageRef: 'unexpected' }),
      () => Promise.reject(new Error('temporary configuration failure')),
      20_000,
      () => 0.5,
    )
    await generic.run()
    expect(generic.completion()).toMatchObject({ retry_after_ms: 375 })

    const provider = deliveryJourney(
      () =>
        Promise.reject(
          new ProviderDeliveryError('rate limited', { retryAfterMs: 2_500, retryable: true }),
        ),
      undefined,
      20_000,
      () => 0,
    )
    await provider.run()
    expect(provider.completion()).toMatchObject({ retry_after_ms: 2_500 })
  })

  it('does not begin a provider send after installation preparation exhausts the deadline', async () => {
    let resolveInstallation: ((installation: typeof testInstallation) => void) | undefined
    const getInstallation = vi.fn(
      () =>
        new Promise<typeof testInstallation>((resolve) => {
          resolveInstallation = resolve
        }),
    )
    const send = vi.fn(() => Promise.resolve({ providerMessageRef: 'unexpected' }))
    const { completion, run } = deliveryJourney(send, getInstallation, 10, undefined, 8)

    await run()

    expect(getInstallation).toHaveBeenCalledOnce()
    expect(send).not.toHaveBeenCalled()
    expect(completion()).toMatchObject({
      outcome: 'retry_wait',
      last_error: { code: 'transient_failure' },
    })
    resolveInstallation?.(testInstallation)
  })

  it('keeps a final claim retryable when shutdown interrupts app acquisition', async () => {
    const acquisition = deferred<undefined>()
    const send = vi.fn(() => Promise.resolve({ providerMessageRef: 'unexpected' }))
    const journey = deliveryJourney(send, undefined, 20_000, undefined, 8, acquisition.promise)
    const running = journey.run()
    await vi.waitFor(() => {
      expect(journey.acquire).toHaveBeenCalledOnce()
    })

    journey.abort()
    await running

    expect(send).not.toHaveBeenCalled()
    expect(journey.completion()).toMatchObject({ outcome: 'retry_wait' })
    acquisition.resolve(undefined)
  })

  it('keeps work retryable when no provider attempt fits in the final claim', async () => {
    const send = vi.fn(() => Promise.resolve({ providerMessageRef: 'unexpected' }))
    const { completion, run } = deliveryJourney(send, undefined, 0, undefined, 8)

    await run()

    expect(send).not.toHaveBeenCalled()
    expect(completion()).toMatchObject({ outcome: 'retry_wait' })
  })

  it('does not blindly retry an unclassified post-send failure', async () => {
    const { completion, run } = deliveryJourney(() =>
      Promise.reject(new Error('connection disappeared after provider accepted the request')),
    )

    await run()

    expect(completion()).toMatchObject({
      outcome: 'unknown',
      last_error: { code: 'outcome_unknown' },
    })
  })

  it('marks an explicitly unknown provider outcome terminal immediately', async () => {
    const { completion, run } = deliveryJourney(() =>
      Promise.reject(
        new ProviderDeliveryError('provider result is ambiguous', {
          outcomeUnknown: true,
          retryable: true,
        }),
      ),
    )

    await run()

    expect(completion()).toMatchObject({ outcome: 'unknown' })
  })

  it('records a definite non-retryable provider rejection as failed', async () => {
    const { completion, run } = deliveryJourney(() =>
      Promise.reject(new ProviderDeliveryError('provider rejected the destination')),
    )

    await run()

    expect(completion()).toMatchObject({
      outcome: 'failed',
      last_error: { code: 'permanent_failure' },
    })
  })

  it('marks a send that crosses its deadline as outcome unknown', async () => {
    const send = vi.fn(() => new Promise<{ providerMessageRef: string }>(() => undefined))
    const { completion, run } = deliveryJourney(send, undefined, 10)

    await run()

    expect(send).toHaveBeenCalledOnce()
    expect(completion()).toMatchObject({
      outcome: 'unknown',
      last_error: { code: 'outcome_unknown' },
    })
  })

  it('bounds provider diagnostics before persisting a terminal result', async () => {
    const { completion, run } = deliveryJourney(() =>
      Promise.reject(new ProviderDeliveryError(`${'界'.repeat(100_000)}\u0000secret`)),
    )

    await run()

    const message = completion().last_error.message
    expect(typeof message).toBe('string')
    expect(Buffer.byteLength(String(message))).toBeLessThanOrEqual(maxDiagnosticMessageBytes)
    expect(message).not.toContain('\u0000')
  })

  it('records a retry and stops promptly when shutdown happens before provider send', async () => {
    const installation = deferred<typeof testInstallation>()
    const send = vi.fn(() => Promise.resolve({ providerMessageRef: 'unexpected' }))
    const journey = deliveryJourney(send, () => installation.promise)
    const running = journey.run()
    await vi.waitFor(() => {
      expect(journey.getInstallation).toHaveBeenCalledOnce()
    })

    journey.abort()
    await running

    expect(send).not.toHaveBeenCalled()
    expect(journey.completion()).toMatchObject({ outcome: 'retry_wait' })
    installation.resolve(testInstallation)
  })

  it('allows an in-flight provider send to finish during graceful shutdown', async () => {
    const sending = deferred<{ providerMessageRef: string }>()
    let providerSignal: AbortSignal | undefined
    const send = vi.fn(
      (_delivery: ChannelConnectorDelivery, context: Parameters<ProviderRuntime['send']>[1]) => {
        providerSignal = context.signal
        return sending.promise
      },
    )
    const journey = deliveryJourney(send)
    const running = journey.run()
    await vi.waitFor(() => {
      expect(send).toHaveBeenCalledOnce()
    })

    journey.abort()
    expect(providerSignal?.aborted).toBe(false)
    sending.resolve({ providerMessageRef: 'provider-message-after-shutdown' })
    await running

    expect(journey.completion()).toMatchObject({
      outcome: 'delivered',
      provider_message_ref: 'provider-message-after-shutdown',
    })
  })
})

function deliveryJourney(
  send: ProviderRuntime['send'],
  getInstallation: () => Promise<typeof testInstallation> = () => Promise.resolve(testInstallation),
  sendTimeoutMs = 20_000,
  random?: () => number,
  attemptCount = 1,
  acquisitionGate?: Promise<void>,
  claimExpiresAt?: string,
): {
  acquire: Mock
  abort: () => void
  completion: () => CompleteChannelConnectorDeliveryRequest
  getInstallation: Mock<() => Promise<typeof testInstallation>>
  run: () => Promise<void>
} {
  const controller = new AbortController()
  const delivery = testDelivery()
  delivery.attempt_count = attemptCount
  if (claimExpiresAt) delivery.claim_expires_at = claimExpiresAt
  const getInstallationMock = vi.fn(getInstallation)
  let completed: CompleteChannelConnectorDeliveryRequest | undefined
  const client = {
    claimDeliveries: vi.fn(() => Promise.resolve([delivery])),
    completeDelivery: vi.fn(
      (_delivery: ChannelConnectorDelivery, value: CompleteChannelConnectorDeliveryRequest) => {
        completed = value
        controller.abort()
        return Promise.resolve()
      },
    ),
  } as unknown as CoreClient
  const runtime: ProviderRuntime = {
    close: () => Promise.resolve(),
    handleWebhook: () => Promise.resolve(new Response()),
    send,
  }
  const acquire = vi.fn(async () => {
    await acquisitionGate
    return {
      configuration: testConfiguration,
      getInstallation: getInstallationMock,
      release: () => Promise.resolve(),
      runtime,
    }
  })
  const registry = {
    acquire,
  } as unknown as AppRuntimeRegistry
  const loop = new DeliveryLoop({
    capabilities: [{ connector_key: 'chat_sdk_v1', provider: 'discord' }],
    claimLimit: 10,
    client,
    completionTimeoutMs: 5_000,
    idlePollMs: 1,
    leaseMs: 30_000,
    logger: noopLogger,
    owner: 'gateway-test',
    random,
    registry,
    sendTimeoutMs,
  })
  return {
    acquire,
    abort: () => {
      controller.abort(new Error('test gateway shutdown'))
    },
    completion: () => {
      if (!completed) throw new Error('delivery was not completed')
      return completed
    },
    getInstallation: getInstallationMock,
    run: () => loop.run(controller.signal),
  }
}

function testDelivery(): ChannelConnectorDelivery {
  return {
    attempt_count: 1,
    app_configuration_revision: 1,
    available_at: new Date().toISOString(),
    claim_expires_at: new Date(Date.now() + 60_000).toISOString(),
    claim_generation: 1,
    claim_token: '00000000-0000-7000-8000-000000000001',
    connector_key: 'chat_sdk_v1',
    created_at: new Date().toISOString(),
    delivery_kind: 'message',
    id: 'idel_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    integration_install_id: 'iin_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    install_configuration_revision: 1,
    integration_target_binding_id: 'ibnd_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    integration_target_id: 'itgt_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    last_error: {},
    payload: {},
    payload_version: 'channel-message.v1',
    provider: 'discord',
    state: 'claimed',
    updated_at: new Date().toISOString(),
  }
}

const testConfiguration = {
  app: {
    configuration_revision: 1,
    connector_key: 'chat_sdk_v1',
    display_name: 'Test',
    id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    provider: 'discord',
    provider_app_ref: 'app-1',
    provider_config: {},
    provider_metadata: {},
    updated_at: new Date().toISOString(),
  },
}

const testInstallation = {
  app_configuration_revision: 1,
  install: {
    configuration_revision: 1,
    id: 'iin_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    provider_account_ref: 'account-1',
    provider_agent_display_name: 'Omnara',
    provider_config: {},
    provider_identity: {},
    provider_metadata: {},
    provider_tenant_id: 'tenant-1',
    updated_at: new Date().toISOString(),
  },
  integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
}

const noopLogger: GatewayLogger = {
  debug: () => undefined,
  error: () => undefined,
  info: () => undefined,
  warn: () => undefined,
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((settle) => {
    resolve = settle
  })
  return { promise, resolve }
}
