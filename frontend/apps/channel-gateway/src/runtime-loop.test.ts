import type { ChannelConnectorRuntimeUnit } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import type { AppRuntimeRegistry, RuntimeHandle } from './app-registry'
import { messageContentBlocks } from './chat-sdk-runtime'
import type { CoreClient } from './core-client'
import { maxDiagnosticMessageBytes } from './diagnostics'
import {
  testMessage,
  testRuntimeHandle,
  testRuntimeUnit,
  unexpectedTestCall,
} from './gateway-test-fixtures'
import { RuntimeLoop, type RuntimeLoopOptions } from './runtime-loop'
import type { GatewayLogger, ProviderRuntime, RuntimeCheckpoint } from './types'
import { WorkByteBudget } from './work-budget'

describe('persistent channel runtime supervision', () => {
  it('fills runtime capacity one exact capability at a time', async () => {
    const controller = new AbortController()
    const capabilities = [
      { connector_key: 'chat_sdk_v1', provider: 'discord' },
      { connector_key: 'custom_v1', provider: 'github' },
    ]
    const first = testRuntimeUnit()
    const second = {
      ...testRuntimeUnit(),
      id: 'irun_bbbbbbbbbbbbbbbbbbbbbbbbbb',
      lease_token: '00000000-0000-7000-8000-000000000002',
    }
    const claims: { capability: (typeof capabilities)[number]; limit: number }[] = []
    const client = {
      ...testClient(),
      claimRuntimeUnits: vi.fn(
        (
          capability: (typeof capabilities)[number],
          _owner: string,
          _leaseMs: number,
          limit: number,
        ) => {
          claims.push({ capability, limit })
          return Promise.resolve(claims.length === 1 ? [first] : [second])
        },
      ),
      heartbeatRuntimeUnit: vi.fn((unit: ChannelConnectorRuntimeUnit) => Promise.resolve(unit)),
      releaseRuntimeUnit: vi.fn(() => Promise.resolve()),
    } satisfies RuntimeLoopOptions['client']
    const runUnit: RuntimeHandle['runUnit'] = async (unit, context) => {
      if (unit.id === second.id) controller.abort(new Error('test complete'))
      await new Promise<void>((resolve) => {
        if (context.signal.aborted) resolve()
        else
          context.signal.addEventListener(
            'abort',
            () => {
              resolve()
            },
            { once: true },
          )
      })
    }
    const runtime: ProviderRuntime = {
      close: () => Promise.resolve(),
      handleWebhook: () => Promise.resolve(new Response()),
      runUnit,
      send: () => Promise.resolve({ providerMessageRef: '' }),
    }
    const registry = {
      acquire: vi.fn(() =>
        Promise.resolve(
          testRuntimeHandle({
            configuration: testConfiguration,
            release: () => Promise.resolve(),
            runUnit,
            runtime,
          }),
        ),
      ),
    } satisfies RuntimeLoopOptions['registry']

    await new RuntimeLoop({
      capabilities,
      claimLimit: 2,
      client,
      idlePollMs: 1,
      leaseMs: 30_000,
      logger: noopLogger,
      owner: 'gateway-test',
      random: () => 0.5,
      reserveWorkBytes: noopWorkReservation,
      registry,
      stopTimeoutMs: 100,
    }).run(controller.signal)

    expect(claims).toEqual([
      { capability: capabilities[0], limit: 1 },
      { capability: capabilities[1], limit: 1 },
    ])
  })

  it('heartbeats the latest checkpoint and releases its fenced lease on shutdown', async () => {
    const controller = new AbortController()
    const unit = testRuntimeUnit()
    let checkpoint: RuntimeCheckpoint | undefined
    let releasedError: Parameters<CoreClient['releaseRuntimeUnit']>[1] | undefined
    const client = {
      ...testClient(),
      claimRuntimeUnits: vi.fn(() => Promise.resolve([unit])),
      heartbeatRuntimeUnit: vi.fn(
        (current: ChannelConnectorRuntimeUnit, _leaseMs: number, value?: RuntimeCheckpoint) => {
          checkpoint = value
          controller.abort(new Error('test shutdown'))
          return Promise.resolve(current)
        },
      ),
      releaseRuntimeUnit: vi.fn(
        (
          _current: ChannelConnectorRuntimeUnit,
          lastError: Parameters<CoreClient['releaseRuntimeUnit']>[1],
        ) => {
          releasedError = lastError
          return Promise.resolve()
        },
      ),
    } satisfies RuntimeLoopOptions['client']
    const runUnit: RuntimeHandle['runUnit'] = async (_current, context) => {
      context.updateCheckpoint({ checkpoint: { sequence: 42 }, version: 1 })
      await new Promise<void>((resolve) => {
        if (context.signal.aborted) resolve()
        else
          context.signal.addEventListener(
            'abort',
            () => {
              resolve()
            },
            { once: true },
          )
      })
    }
    const runtime: ProviderRuntime = {
      close: () => Promise.resolve(),
      handleWebhook: () => Promise.resolve(new Response()),
      runUnit,
      send: () => Promise.resolve({ providerMessageRef: '' }),
    }
    const registry = {
      acquire: vi.fn(() =>
        Promise.resolve(
          testRuntimeHandle({
            configuration: testConfiguration,
            release: () => Promise.resolve(),
            runUnit: (
              current: ChannelConnectorRuntimeUnit,
              context: Parameters<RuntimeHandle['runUnit']>[1],
            ) => runUnit(current, context),
            runtime,
          }),
        ),
      ),
    } satisfies RuntimeLoopOptions['registry']

    await new RuntimeLoop({
      capabilities: [{ connector_key: 'chat_sdk_v1', provider: 'discord' }],
      claimLimit: 1,
      client,
      idlePollMs: 1,
      leaseMs: 30,
      logger: noopLogger,
      owner: 'gateway-test',
      reserveWorkBytes: noopWorkReservation,
      registry,
      stopTimeoutMs: 100,
    }).run(controller.signal)

    expect(checkpoint).toEqual({ checkpoint: { sequence: 42 }, version: 1 })
    expect(releasedError).toEqual({})
  })

  it('reports an unsupported persistent mode without starting provider work', async () => {
    const controller = new AbortController()
    let releasedError: Parameters<CoreClient['releaseRuntimeUnit']>[1] | undefined
    const client = {
      ...testClient(),
      claimRuntimeUnits: vi.fn(() => Promise.resolve([testRuntimeUnit()])),
      releaseRuntimeUnit: vi.fn(
        (
          _unit: ChannelConnectorRuntimeUnit,
          lastError: Parameters<CoreClient['releaseRuntimeUnit']>[1],
        ) => {
          releasedError = lastError
          controller.abort()
          return Promise.resolve()
        },
      ),
    } satisfies RuntimeLoopOptions['client']
    const registry = {
      acquire: vi.fn(() =>
        Promise.resolve(
          testRuntimeHandle({
            configuration: testConfiguration,
            release: () => Promise.resolve(),
            runUnit: vi.fn(),
            runtime: {
              close: () => Promise.resolve(),
              handleWebhook: () => Promise.resolve(new Response()),
              send: () => Promise.resolve({ providerMessageRef: '' }),
            } satisfies ProviderRuntime,
          }),
        ),
      ),
    } satisfies RuntimeLoopOptions['registry']

    await new RuntimeLoop({
      capabilities: [{ connector_key: 'chat_sdk_v1', provider: 'discord' }],
      claimLimit: 1,
      client,
      idlePollMs: 1,
      leaseMs: 30,
      logger: noopLogger,
      owner: 'gateway-test',
      reserveWorkBytes: noopWorkReservation,
      registry,
      stopTimeoutMs: 100,
    }).run(controller.signal)

    expect(releasedError).toMatchObject({ code: 'runtime_not_supported' })
  })

  it('bounds provider diagnostics before releasing a failed runtime', async () => {
    const controller = new AbortController()
    let releasedError: Parameters<CoreClient['releaseRuntimeUnit']>[1] | undefined
    const client = {
      ...testClient(),
      claimRuntimeUnits: vi.fn(() => Promise.resolve([testRuntimeUnit()])),
      heartbeatRuntimeUnit: vi.fn((unit: ChannelConnectorRuntimeUnit) => Promise.resolve(unit)),
      releaseRuntimeUnit: vi.fn(
        (
          _unit: ChannelConnectorRuntimeUnit,
          lastError: Parameters<CoreClient['releaseRuntimeUnit']>[1],
        ) => {
          releasedError = lastError
          controller.abort(new Error('test complete'))
          return Promise.resolve()
        },
      ),
    } satisfies RuntimeLoopOptions['client']
    const runUnit = vi.fn(() => Promise.reject(new Error(`${'界'.repeat(100_000)}\u0000secret`)))
    const runtime: ProviderRuntime = {
      close: () => Promise.resolve(),
      handleWebhook: () => Promise.resolve(new Response()),
      runUnit,
      send: () => Promise.resolve({ providerMessageRef: '' }),
    }
    const registry = {
      acquire: vi.fn(() =>
        Promise.resolve(
          testRuntimeHandle({
            configuration: testConfiguration,
            release: () => Promise.resolve(),
            runUnit,
            runtime,
          }),
        ),
      ),
    } satisfies RuntimeLoopOptions['registry']

    await new RuntimeLoop({
      capabilities: [{ connector_key: 'chat_sdk_v1', provider: 'discord' }],
      claimLimit: 1,
      client,
      idlePollMs: 1,
      leaseMs: 30_000,
      logger: noopLogger,
      owner: 'gateway-test',
      reserveWorkBytes: noopWorkReservation,
      registry,
      stopTimeoutMs: 100,
    }).run(controller.signal)

    const message = String(releasedError?.message)
    expect(releasedError).toMatchObject({ code: 'runtime_failed' })
    expect(Buffer.byteLength(message)).toBeLessThanOrEqual(maxDiagnosticMessageBytes)
    expect(message).not.toContain('\u0000')
  })

  it('flushes a checkpoint when provider work settles before the first heartbeat', async () => {
    const controller = new AbortController()
    const checkpoint: RuntimeCheckpoint = { checkpoint: { sequence: 7 }, version: 1 }
    let releasedCheckpoint: RuntimeCheckpoint | undefined
    const client = {
      ...testClient(),
      claimRuntimeUnits: vi.fn(() => Promise.resolve([testRuntimeUnit()])),
      releaseRuntimeUnit: vi.fn(
        (
          _unit: ChannelConnectorRuntimeUnit,
          _lastError: Parameters<CoreClient['releaseRuntimeUnit']>[1],
          value?: RuntimeCheckpoint,
        ) => {
          releasedCheckpoint = value
          controller.abort()
          return Promise.resolve()
        },
      ),
    } satisfies RuntimeLoopOptions['client']
    const runUnit: RuntimeHandle['runUnit'] = (_unit, context) => {
      context.updateCheckpoint(checkpoint)
      return Promise.resolve()
    }
    const registry = {
      acquire: vi.fn(() =>
        Promise.resolve(
          testRuntimeHandle({
            configuration: testConfiguration,
            release: () => Promise.resolve(),
            runUnit,
            runtime: {
              close: () => Promise.resolve(),
              handleWebhook: () => Promise.resolve(new Response()),
              runUnit,
              send: () => Promise.resolve({ providerMessageRef: '' }),
            } satisfies ProviderRuntime,
          }),
        ),
      ),
    } satisfies RuntimeLoopOptions['registry']

    await new RuntimeLoop({
      capabilities: [{ connector_key: 'chat_sdk_v1', provider: 'discord' }],
      claimLimit: 1,
      client,
      idlePollMs: 1,
      leaseMs: 30_000,
      logger: noopLogger,
      owner: 'gateway-test',
      reserveWorkBytes: noopWorkReservation,
      registry,
      stopTimeoutMs: 100,
    }).run(controller.signal)

    expect(releasedCheckpoint).toEqual(checkpoint)
  })

  it('heartbeats during delayed initialization and abandons work after renewal timeouts', async () => {
    let leaseTimeMs = 1_000
    const controller = new AbortController()
    const unit = testRuntimeUnit()
    const acquisition = deferred<Awaited<ReturnType<AppRuntimeRegistry['acquire']>>>()
    const handleRelease = vi.fn(() => Promise.resolve())
    const runUnit = vi.fn(() => Promise.resolve())
    let releasedError: Parameters<CoreClient['releaseRuntimeUnit']>[1] | undefined
    const heartbeatRuntimeUnit = vi.fn(
      (
        _unit: ChannelConnectorRuntimeUnit,
        _leaseMs: number,
        _checkpoint: RuntimeCheckpoint | undefined,
        signal?: AbortSignal,
      ) =>
        new Promise<ChannelConnectorRuntimeUnit>((_resolve, reject) => {
          if (!signal) {
            reject(new Error('test heartbeat requires an abort signal'))
            return
          }
          const rejectAbort = (): void => {
            leaseTimeMs += 10
            reject(
              signal.reason instanceof Error ? signal.reason : new Error('test heartbeat aborted'),
            )
          }
          if (signal.aborted) rejectAbort()
          else signal.addEventListener('abort', rejectAbort, { once: true })
        }),
    )
    const client = {
      ...testClient(),
      claimRuntimeUnits: vi.fn(() => Promise.resolve([unit])),
      heartbeatRuntimeUnit,
      releaseRuntimeUnit: vi.fn(
        (
          _current: ChannelConnectorRuntimeUnit,
          lastError: Parameters<CoreClient['releaseRuntimeUnit']>[1],
        ) => {
          releasedError = lastError
          controller.abort(new Error('test complete'))
          return Promise.resolve()
        },
      ),
    } satisfies RuntimeLoopOptions['client']
    const registry = {
      acquire: vi.fn(() => acquisition.promise),
    } satisfies RuntimeLoopOptions['registry']

    // Advance the lease clock on real heartbeat timeouts so runner load cannot skip retries.
    const now = vi.spyOn(Date, 'now').mockImplementation(() => leaseTimeMs)
    try {
      await new RuntimeLoop({
        capabilities: [{ connector_key: 'chat_sdk_v1', provider: 'discord' }],
        claimLimit: 1,
        client,
        idlePollMs: 1,
        leaseMs: 30,
        logger: noopLogger,
        owner: 'gateway-test',
        reserveWorkBytes: noopWorkReservation,
        registry,
        stopTimeoutMs: 100,
      }).run(controller.signal)
    } finally {
      now.mockRestore()
    }

    expect(heartbeatRuntimeUnit.mock.calls.length).toBeGreaterThan(1)
    expect(releasedError).toMatchObject({ code: 'runtime_lease_lost' })
    expect(runUnit).not.toHaveBeenCalled()

    acquisition.resolve(
      testRuntimeHandle({
        configuration: testConfiguration,
        handleWebhook: vi.fn(),
        release: handleRelease,
        resolveInstallation: vi.fn(),
        runUnit,
        runtime: {
          close: () => Promise.resolve(),
          handleWebhook: () => Promise.resolve(new Response()),
          runUnit,
          send: () => Promise.resolve({ providerMessageRef: '' }),
        },
      }),
    )
    await vi.waitFor(() => {
      expect(handleRelease).toHaveBeenCalledOnce()
    })
  })

  it('cancels active provider work immediately when its lease heartbeat is fenced', async () => {
    const controller = new AbortController()
    const unit = testRuntimeUnit()
    let providerSignal: AbortSignal | undefined
    let releasedError: Parameters<CoreClient['releaseRuntimeUnit']>[1] | undefined
    const client = {
      ...testClient(),
      claimRuntimeUnits: vi.fn(() => Promise.resolve([unit])),
      heartbeatRuntimeUnit: vi.fn(() => Promise.reject(new Error('runtime lease was fenced'))),
      releaseRuntimeUnit: vi.fn(
        (
          _current: ChannelConnectorRuntimeUnit,
          lastError: Parameters<CoreClient['releaseRuntimeUnit']>[1],
        ) => {
          releasedError = lastError
          controller.abort(new Error('test complete'))
          return Promise.resolve()
        },
      ),
    } satisfies RuntimeLoopOptions['client']
    const runUnit = vi.fn<RuntimeHandle['runUnit']>(async (_current, context) => {
      providerSignal = context.signal
      await new Promise<void>((resolve) => {
        if (context.signal.aborted) resolve()
        else
          context.signal.addEventListener(
            'abort',
            () => {
              resolve()
            },
            { once: true },
          )
      })
    })
    const runtime: ProviderRuntime = {
      close: () => Promise.resolve(),
      handleWebhook: () => Promise.resolve(new Response()),
      runUnit,
      send: () => Promise.resolve({ providerMessageRef: '' }),
    }
    const registry = {
      acquire: vi.fn(() =>
        Promise.resolve(
          testRuntimeHandle({
            configuration: testConfiguration,
            release: () => Promise.resolve(),
            runUnit,
            runtime,
          }),
        ),
      ),
    } satisfies RuntimeLoopOptions['registry']

    await new RuntimeLoop({
      capabilities: [{ connector_key: 'chat_sdk_v1', provider: 'discord' }],
      claimLimit: 1,
      client,
      idlePollMs: 1,
      leaseMs: 30,
      logger: noopLogger,
      owner: 'gateway-test',
      reserveWorkBytes: noopWorkReservation,
      registry,
      stopTimeoutMs: 100,
    }).run(controller.signal)

    expect(runUnit).toHaveBeenCalledOnce()
    expect(providerSignal?.aborted).toBe(true)
    expect(releasedError).toMatchObject({ code: 'runtime_lease_lost' })
  })

  it('keeps provider work alive after a timed-out heartbeat', async () => {
    const controller = new AbortController()
    const unit = testRuntimeUnit()
    let providerSignal: AbortSignal | undefined
    let abortedBeforeRecovery = true
    const heartbeatRuntimeUnit = vi
      .fn<CoreClient['heartbeatRuntimeUnit']>()
      .mockImplementationOnce(
        (
          _unit: ChannelConnectorRuntimeUnit,
          _leaseMs: number,
          _checkpoint?: RuntimeCheckpoint,
          signal?: AbortSignal,
        ) =>
          new Promise<ChannelConnectorRuntimeUnit>((_resolve, reject) => {
            if (!signal) {
              reject(new Error('test heartbeat requires an abort signal'))
              return
            }
            const rejectAbort = (): void => {
              reject(
                signal.reason instanceof Error
                  ? signal.reason
                  : new Error('test heartbeat timed out'),
              )
            }
            if (signal.aborted) rejectAbort()
            else signal.addEventListener('abort', rejectAbort, { once: true })
          }),
      )
      .mockImplementationOnce(() => {
        abortedBeforeRecovery = providerSignal?.aborted ?? true
        controller.abort(new Error('test complete'))
        return Promise.resolve(unit)
      })
    const client = {
      ...testClient(),
      claimRuntimeUnits: vi.fn(() => Promise.resolve([unit])),
      heartbeatRuntimeUnit,
      releaseRuntimeUnit: vi.fn(() => Promise.resolve()),
    } satisfies RuntimeLoopOptions['client']
    const runUnit = vi.fn<RuntimeHandle['runUnit']>(async (_current, context) => {
      providerSignal = context.signal
      await new Promise<void>((resolve) => {
        if (context.signal.aborted) resolve()
        else
          context.signal.addEventListener(
            'abort',
            () => {
              resolve()
            },
            { once: true },
          )
      })
    })
    const registry = {
      acquire: vi.fn(() =>
        Promise.resolve(
          testRuntimeHandle({
            configuration: testConfiguration,
            release: () => Promise.resolve(),
            runUnit,
            runtime: {
              close: () => Promise.resolve(),
              handleWebhook: () => Promise.resolve(new Response()),
              runUnit,
              send: () => Promise.resolve({ providerMessageRef: '' }),
            },
          }),
        ),
      ),
    } satisfies RuntimeLoopOptions['registry']

    await new RuntimeLoop({
      capabilities: [{ connector_key: 'chat_sdk_v1', provider: 'discord' }],
      claimLimit: 1,
      client,
      idlePollMs: 1,
      leaseMs: 120,
      logger: noopLogger,
      owner: 'gateway-test',
      reserveWorkBytes: noopWorkReservation,
      registry,
      stopTimeoutMs: 100,
    }).run(controller.signal)

    expect(heartbeatRuntimeUnit).toHaveBeenCalledTimes(2)
    expect(abortedBeforeRecovery).toBe(false)
  })

  it('shares one media-work budget across concurrent persistent runtime units', async () => {
    const controller = new AbortController()
    const gate = deferred<undefined>()
    const budget = new WorkByteBudget(10)
    const first = testRuntimeUnit()
    const second = {
      ...testRuntimeUnit(),
      id: 'irun_bbbbbbbbbbbbbbbbbbbbbbbbbb',
      lease_token: '00000000-0000-7000-8000-000000000002',
    }
    let claims = 0
    const releases: Parameters<CoreClient['releaseRuntimeUnit']>[1][] = []
    const client = {
      ...testClient(),
      claimRuntimeUnits: vi.fn(() => Promise.resolve(claims++ === 0 ? [first, second] : [])),
      heartbeatRuntimeUnit: vi.fn((unit: ChannelConnectorRuntimeUnit) => Promise.resolve(unit)),
      releaseRuntimeUnit: vi.fn(
        (
          _unit: ChannelConnectorRuntimeUnit,
          lastError: Parameters<CoreClient['releaseRuntimeUnit']>[1],
        ) => {
          releases.push(lastError)
          if (releases.length === 1) gate.resolve(undefined)
          if (releases.length === 2) controller.abort(new Error('test complete'))
          return Promise.resolve()
        },
      ),
    } satisfies RuntimeLoopOptions['client']
    const message = testMessage({
      attachments: [
        {
          fetchData: () => Promise.resolve(Buffer.from([1, 2])),
          type: 'image',
          mimeType: 'image/png',
          name: 'tiny.png',
          size: 2,
        },
      ],
      text: '',
    })
    let admitted = 0
    const runUnit: RuntimeHandle['runUnit'] = async (_unit, context) => {
      const reservations: ReturnType<typeof context.reserveWorkBytes>[] = []
      try {
        await messageContentBlocks(message, 4, 4, {
          fetchAttachmentData: (attachment) => {
            if (!attachment.fetchData) throw new Error('missing test attachment loader')
            return attachment.fetchData()
          },
          reserveWorkBytes: (bytes) => {
            const reservation = context.reserveWorkBytes(bytes)
            reservations.push(reservation)
            return reservation
          },
        })
        admitted++
        await gate.promise
      } finally {
        for (const reservation of reservations) reservation.release()
      }
    }
    const runtime: ProviderRuntime = {
      close: () => Promise.resolve(),
      handleWebhook: () => Promise.resolve(new Response()),
      runUnit,
      send: () => Promise.resolve({ providerMessageRef: '' }),
    }
    const registry = {
      acquire: vi.fn(() =>
        Promise.resolve(
          testRuntimeHandle({
            configuration: testConfiguration,
            release: () => Promise.resolve(),
            runUnit,
            runtime,
          }),
        ),
      ),
    } satisfies RuntimeLoopOptions['registry']

    await new RuntimeLoop({
      capabilities: [{ connector_key: 'chat_sdk_v1', provider: 'discord' }],
      claimLimit: 2,
      client,
      idlePollMs: 1,
      leaseMs: 30_000,
      logger: noopLogger,
      owner: 'gateway-test',
      reserveWorkBytes: budget.reserve,
      registry,
      stopTimeoutMs: 100,
    }).run(controller.signal)

    expect(admitted).toBe(1)
    expect(releases).toHaveLength(2)
    expect(releases.some((error) => error.code === 'runtime_failed')).toBe(true)
    expect(budget.usedBytes).toBe(0)
  })

  it('releases reservations abandoned by a failed persistent runtime', async () => {
    const controller = new AbortController()
    const budget = new WorkByteBudget(10)
    const first = testRuntimeUnit()
    const second = {
      ...testRuntimeUnit(),
      id: 'irun_bbbbbbbbbbbbbbbbbbbbbbbbbb',
      lease_token: '00000000-0000-7000-8000-000000000002',
    }
    let claim = 0
    let secondAdmitted = false
    const client = {
      ...testClient(),
      claimRuntimeUnits: vi.fn(() =>
        Promise.resolve(claim++ === 0 ? [first] : claim === 2 ? [second] : []),
      ),
      heartbeatRuntimeUnit: vi.fn((unit: ChannelConnectorRuntimeUnit) => Promise.resolve(unit)),
      releaseRuntimeUnit: vi.fn((unit: ChannelConnectorRuntimeUnit) => {
        if (unit.id === second.id) controller.abort(new Error('test complete'))
        return Promise.resolve()
      }),
    } satisfies RuntimeLoopOptions['client']
    const runUnit: RuntimeHandle['runUnit'] = (unit, context) => {
      const reservation = context.reserveWorkBytes(10)
      if (unit.id === first.id) {
        return Promise.reject(new Error('provider abandoned its reservation'))
      }
      secondAdmitted = true
      reservation.release()
      return Promise.resolve()
    }
    const runtime: ProviderRuntime = {
      close: () => Promise.resolve(),
      handleWebhook: () => Promise.resolve(new Response()),
      runUnit,
      send: () => Promise.resolve({ providerMessageRef: '' }),
    }
    const registry = {
      acquire: vi.fn(() =>
        Promise.resolve(
          testRuntimeHandle({
            configuration: testConfiguration,
            release: () => Promise.resolve(),
            runUnit,
            runtime,
          }),
        ),
      ),
    } satisfies RuntimeLoopOptions['registry']

    await new RuntimeLoop({
      capabilities: [{ connector_key: 'chat_sdk_v1', provider: 'discord' }],
      claimLimit: 1,
      client,
      idlePollMs: 1,
      leaseMs: 30_000,
      logger: noopLogger,
      owner: 'gateway-test',
      reserveWorkBytes: budget.reserve,
      registry,
      stopTimeoutMs: 100,
    }).run(controller.signal)

    expect(secondAdmitted).toBe(true)
    expect(budget.usedBytes).toBe(0)
  })
})

function testClient() {
  return {
    claimRuntimeUnits: vi.fn<CoreClient['claimRuntimeUnits']>(unexpectedTestCall),
    heartbeatRuntimeUnit: vi.fn<CoreClient['heartbeatRuntimeUnit']>(unexpectedTestCall),
    releaseRuntimeUnit: vi.fn<CoreClient['releaseRuntimeUnit']>(unexpectedTestCall),
  } satisfies RuntimeLoopOptions['client']
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

const noopLogger: GatewayLogger = {
  debug: () => undefined,
  error: () => undefined,
  info: () => undefined,
  warn: () => undefined,
}

function noopWorkReservation() {
  return { release: () => undefined, resize: () => undefined }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((settle) => {
    resolve = settle
  })
  return { promise, resolve }
}
