import type { ChannelConnectorEventReceipt } from '@omnara/sdk'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CoreClient } from './core-client'
import { ReceiptConsumer, type ReceiptConsumerOptions } from './receipt-consumer'
import { initialReceiptWorkBytes } from './receipt-http'
import { type ReceiptBehaviorContext, ReceiptBehaviorError } from './types'
import { WorkByteBudget } from './work-budget'

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-09-14T12:00:00Z'))
})
afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('receipt consumer', () => {
  it('passes the same scoped lease to behavior and completion, without replaying intake', async () => {
    const { options, controller, client, behavior, workBudget } = fixture()
    const claimed = receipt()
    client.claimNextEvent.mockResolvedValueOnce(claimed)
    client.completeEvent.mockImplementation(() => {
      controller.abort()
      return Promise.resolve()
    })
    await new ReceiptConsumer(options).run(controller.signal)
    const delivered = behavior.mock.calls[0]?.[0]
    expect(delivered).toEqual(claimed)
    expect(Object.isFrozen(delivered)).toBe(true)
    expect(client.completeEvent).toHaveBeenCalledWith(
      delivered,
      { state: 'completed' },
      expect.any(AbortSignal),
    )
    expect(client.completeEvent.mock.calls[0]?.[0]).toBe(delivered)
    expect(behavior).toHaveBeenCalledOnce()
    expect(client.claimNextEvent).toHaveBeenCalledOnce()
    expect(workBudget.usedBytes).toBe(0)
  })

  it('claims only free slots and replenishes one without waiting for the other active event', async () => {
    const { options, controller, client, behavior, workBudget } = fixture()
    options.maxConcurrentEvents = 2
    const first = deferred()
    const second = deferred()
    client.claimNextEvent.mockResolvedValueOnce(receipt()).mockResolvedValueOnce(receipt(2))
    behavior
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise)
    const running = new ReceiptConsumer(options).run(controller.signal)
    await flush()
    expect(client.claimNextEvent).toHaveBeenCalledTimes(2)
    expect(behavior).toHaveBeenCalledTimes(2)
    expect(workBudget.usedBytes).toBe(initialReceiptWorkBytes * 2)
    first.resolve()
    await flush()
    expect(client.claimNextEvent).toHaveBeenCalledTimes(3)
    expect(behavior).toHaveBeenCalledTimes(2)
    controller.abort()
    second.resolve()
    await running
    expect(workBudget.usedBytes).toBe(0)
  })

  it('reserves shared memory before a claim and waits for admission when occupied', async () => {
    const { options, controller, client, workBudget } = fixture()
    const occupied = workBudget.reserve(workBudget.limitBytes)
    const running = new ReceiptConsumer(options).run(controller.signal)
    await vi.advanceTimersByTimeAsync(20)
    expect(client.claimNextEvent).not.toHaveBeenCalled()
    occupied.release()
    await vi.advanceTimersByTimeAsync(10)
    expect(client.claimNextEvent).toHaveBeenCalled()
    controller.abort()
    await running
    expect(workBudget.usedBytes).toBe(0)
  })

  it('rotates exact capability pairs without overlapping claims in one slot', async () => {
    const { options, controller, client } = fixture()
    options.capabilities = [
      { connector_key: 'one', provider: 'slack' },
      { connector_key: 'two', provider: 'slack' },
    ]
    const running = new ReceiptConsumer(options).run(controller.signal)
    await vi.advanceTimersByTimeAsync(20)
    controller.abort()
    await running
    expect(client.claimNextEvent.mock.calls.slice(0, 3).map(([capability]) => capability)).toEqual([
      options.capabilities[0],
      options.capabilities[1],
      options.capabilities[0],
    ])
  })

  it.each([
    {
      cause: new ReceiptBehaviorError(true),
      attempt: 2,
      state: 'pending',
      code: 'retryable_failure',
    },
    {
      cause: new ReceiptBehaviorError(true),
      attempt: 3,
      state: 'failed',
      code: 'retry_budget_exhausted',
    },
    {
      cause: new ReceiptBehaviorError(false),
      attempt: 1,
      state: 'failed',
      code: 'permanent_failure',
    },
    {
      cause: new Error('secret provider payload'),
      attempt: 1,
      state: 'failed',
      code: 'behavior_outcome_unknown',
    },
    {
      cause: new DOMException('unclassified provider abort', 'AbortError'),
      attempt: 1,
      state: 'failed',
      code: 'behavior_outcome_unknown',
    },
  ])(
    'classifies $code using the durable attempt count, with no local behavior retry',
    async ({ cause, attempt, state, code }) => {
      const { options, controller, client, behavior, logger } = fixture()
      client.claimNextEvent.mockResolvedValueOnce({ ...receipt(), attempt_count: attempt })
      behavior.mockRejectedValue(cause)
      client.completeEvent.mockImplementation(() => {
        controller.abort()
        return Promise.resolve()
      })
      await new ReceiptConsumer(options).run(controller.signal)
      expect(behavior).toHaveBeenCalledOnce()
      expect(client.completeEvent.mock.calls[0]?.[1]).toEqual({ state, last_error: { code } })
      expect(logger.error).not.toHaveBeenCalled()
    },
  )

  it.each([0, 60_000, 86_400_000])(
    'forwards a valid %sms provider hint only on pending completion',
    async (retryAfterMs) => {
      const f = fixture()
      f.client.claimNextEvent.mockResolvedValueOnce(receipt())
      f.behavior.mockRejectedValue(new ReceiptBehaviorError(true, retryAfterMs))
      f.client.completeEvent.mockImplementation(() => {
        f.controller.abort()
        return Promise.resolve()
      })
      await new ReceiptConsumer(f.options).run(f.controller.signal)
      expect(f.client.completeEvent).toHaveBeenCalledOnce()
      expect(f.client.completeEvent.mock.calls[0]?.[1]).toEqual({
        state: 'pending',
        retry_after_ms: retryAfterMs,
        last_error: { code: 'retryable_failure' },
      })
    },
  )

  it.each([null, -1, 0.5, NaN, Infinity, 86_400_001, '60000'])(
    'fails closed for invalid provider hint %s instead of scheduling an early retry',
    async (retryAfterMs) => {
      const f = fixture()
      f.client.claimNextEvent.mockResolvedValueOnce(receipt())
      f.behavior.mockRejectedValue(Object.assign(new ReceiptBehaviorError(true), { retryAfterMs }))
      f.client.completeEvent.mockImplementation(() => {
        f.controller.abort()
        return Promise.resolve()
      })
      await new ReceiptConsumer(f.options).run(f.controller.signal)
      expect(f.client.completeEvent.mock.calls[0]?.[1]).toEqual({
        state: 'failed',
        last_error: { code: 'invalid_retry_hint' },
      })
      expect(f.client.completeEvent).toHaveBeenCalledOnce()
      expect(f.behavior).toHaveBeenCalledOnce()
    },
  )

  it.each([
    { retryable: false, attempt: 1, code: 'permanent_failure' },
    { retryable: true, attempt: 3, code: 'retry_budget_exhausted' },
  ])('does not attach a scheduling hint to $code', async ({ retryable, attempt, code }) => {
    const f = fixture()
    f.client.claimNextEvent.mockResolvedValueOnce({ ...receipt(), attempt_count: attempt })
    f.behavior.mockRejectedValue(new ReceiptBehaviorError(retryable, 60_000))
    f.client.completeEvent.mockImplementation(() => {
      f.controller.abort()
      return Promise.resolve()
    })
    await new ReceiptConsumer(f.options).run(f.controller.signal)
    expect(f.client.completeEvent.mock.calls[0]?.[1]).toEqual({
      state: 'failed',
      last_error: { code },
    })
  })

  it('reports a minute-long retry hint through one completion POST within the existing lease budget', async () => {
    const f = fixture()
    const claimed = { ...receipt(), receipt_id: 'irec_aaaaaaaaaaaaaaaaaaaaaaaaaa' }
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(Response.json(claimed))
      .mockImplementationOnce(() => {
        f.controller.abort()
        return Promise.resolve(Response.json({ receipt_id: claimed.receipt_id, state: 'pending' }))
      })
    f.options.client = new CoreClient({
      baseUrl: 'https://core.example.test/api/v1',
      token: 'fixture',
      fetch,
    })
    f.behavior.mockRejectedValue(new ReceiptBehaviorError(true, 60_000))
    const started = Date.now()
    await new ReceiptConsumer(f.options).run(f.controller.signal)
    expect(Date.now() - started).toBeLessThan(f.options.completionTimeoutMs)
    expect(f.behavior).toHaveBeenCalledOnce()
    expect(fetch).toHaveBeenCalledTimes(2) // One claim, one completion; no local sleep/retry.
    const request = fetch.mock.calls[1]?.[0]
    if (!(request instanceof Request)) throw new Error('missing completion request')
    expect(request.method).toBe('POST')
    expect(request.url).toContain(`/events/${claimed.receipt_id}/complete`)
    expect(await request.json()).toEqual({
      state: 'pending',
      retry_after_ms: 60_000,
      last_error: { code: 'retryable_failure' },
      lease_token: claimed.lease_token,
      lease_generation: claimed.lease_generation,
    })
    expect(f.workBudget.usedBytes).toBe(0)
  })

  it('terminalizes a receipt already over the deployment attempt budget without behavior', async () => {
    const { options, controller, client, behavior } = fixture()
    client.claimNextEvent.mockResolvedValueOnce({ ...receipt(), attempt_count: 4 })
    client.completeEvent.mockImplementation(() => {
      controller.abort()
      return Promise.resolve()
    })
    await new ReceiptConsumer(options).run(controller.signal)
    expect(behavior).not.toHaveBeenCalled()
    expect(client.completeEvent.mock.calls[0]?.[1].state).toBe('failed')
  })

  it('does not rerun behavior or completion when completion outcome is unknown', async () => {
    const { options, controller, client, behavior, logger } = fixture()
    client.claimNextEvent.mockResolvedValueOnce(receipt())
    client.completeEvent.mockImplementation(() => {
      controller.abort()
      return Promise.reject(new Error('secret lost response'))
    })
    await new ReceiptConsumer(options).run(controller.signal)
    expect(behavior).toHaveBeenCalledOnce()
    expect(client.completeEvent).toHaveBeenCalledOnce()
    expect(logger.error).toHaveBeenCalledWith('channel receipt completion failed', {
      receipt_id: receipt().receipt_id,
      state: 'completed',
    })
  })

  it('fits the behavior deadline inside the actual lease and aborts actual I/O', async () => {
    const { options, controller, client, behavior } = fixture()
    client.claimNextEvent.mockResolvedValueOnce({
      ...receipt(),
      lease_expires_at: new Date(Date.now() + 150).toISOString(),
    })
    let context: ReceiptBehaviorContext | undefined
    behavior.mockImplementation((_receipt, incoming) => {
      context = incoming
      return untilAbort(incoming.signal)
    })
    client.completeEvent.mockImplementation(() => {
      controller.abort()
      return Promise.resolve()
    })
    const running = new ReceiptConsumer(options).run(controller.signal)
    await flush()
    expect(context?.deadlineMs).toBe(Date.now() + 50)
    await vi.advanceTimersByTimeAsync(50)
    await running
    expect(context?.signal.aborted).toBe(true)
    expect(client.completeEvent.mock.calls[0]?.[1]).toEqual({
      state: 'pending',
      last_error: { code: 'retryable_failure' },
    })
  })

  it('aborts behavior on shutdown but still attempts one bounded fenced completion', async () => {
    const { options, controller, client, behavior, workBudget } = fixture()
    client.claimNextEvent.mockResolvedValueOnce(receipt())
    let behaviorSignal: AbortSignal | undefined
    let completionSignal: AbortSignal | undefined
    behavior.mockImplementation((_receipt, { signal }) => {
      behaviorSignal = signal
      return untilAbort(signal)
    })
    client.completeEvent.mockImplementation((_receipt, _completion, signal) => {
      completionSignal = signal
      expect(signal?.aborted).toBe(false)
      return Promise.resolve()
    })
    const running = new ReceiptConsumer(options).run(controller.signal)
    await flush()
    controller.abort(new Error('private shutdown diagnostic'))
    await running
    expect(behaviorSignal?.aborted).toBe(true)
    expect(completionSignal?.aborted).toBe(true)
    expect(client.completeEvent).toHaveBeenCalledOnce()
    expect(client.completeEvent.mock.calls[0]?.[1]).toEqual({
      state: 'pending',
      last_error: { code: 'retryable_failure' },
    })
    expect(workBudget.usedBytes).toBe(0)
  })

  it.each(['shutdown', 'deadline'] as const)(
    'replays partial admission after %s through a fresh consumer without duplicate inputs',
    async (interruption) => {
      const first = fixture()
      const original = receipt()
      first.client.claimNextEvent.mockResolvedValueOnce(original)
      // Model core's durable semantic admission: a second call with the same
      // agent/key returns the accepted input even under a new receipt lease.
      const accepted = new Set<string>()
      const inserted: string[] = []
      const attempts: { recipient: string; generation: number }[] = []
      const inputKey = 'slack:message:T1:C1:111.222'
      const admit = (recipient: string, queued: Readonly<ChannelConnectorEventReceipt>) => {
        attempts.push({ recipient, generation: queued.lease_generation })
        const semanticKey = `${recipient}:${inputKey}`
        if (!accepted.has(semanticKey)) {
          accepted.add(semanticKey)
          inserted.push(recipient)
        }
      }
      first.behavior.mockImplementation(async (queued, { signal }) => {
        admit('agent-one', queued)
        try {
          await untilAbort(signal)
        } catch {
          // The consumer's race observes its abort reason before this rejection.
          throw new ReceiptBehaviorError(true)
        }
      })
      first.client.completeEvent.mockImplementation(() => {
        first.controller.abort()
        return Promise.resolve()
      })
      const firstRun = new ReceiptConsumer(first.options).run(first.controller.signal)
      await flush()
      expect(inserted).toEqual(['agent-one'])
      if (interruption === 'shutdown') first.controller.abort()
      else await vi.advanceTimersByTimeAsync(first.options.behaviorTimeoutMs)
      await firstRun
      expect(first.client.completeEvent).toHaveBeenCalledOnce()
      expect(first.client.completeEvent.mock.calls[0]?.[1]).toEqual({
        state: 'pending',
        last_error: { code: 'retryable_failure' },
      })
      expect(first.behavior).toHaveBeenCalledOnce()
      expect(first.workBudget.usedBytes).toBe(0)

      // Core reclaims the same durable receipt with a new fenced lease. No
      // process-local progress or payload rewrite is carried to the new consumer.
      const restarted = fixture()
      const reclaimed: ChannelConnectorEventReceipt = {
        ...original,
        attempt_count: original.attempt_count + 1,
        lease_generation: original.lease_generation + 1,
        lease_token: '01994550-1234-7123-8123-123456789abd',
        lease_expires_at: new Date(Date.now() + restarted.options.leaseMs).toISOString(),
      }
      restarted.client.claimNextEvent.mockResolvedValueOnce(reclaimed)
      restarted.behavior.mockImplementation((queued) => {
        admit('agent-one', queued)
        admit('agent-two', queued)
        return Promise.resolve()
      })
      restarted.client.completeEvent.mockImplementation(() => {
        restarted.controller.abort()
        return Promise.resolve()
      })
      await new ReceiptConsumer(restarted.options).run(restarted.controller.signal)
      expect(restarted.behavior.mock.calls[0]?.[0]).toEqual(reclaimed)
      expect(restarted.behavior.mock.calls[0]?.[0].payload).toBe(original.payload)
      expect(restarted.client.completeEvent).toHaveBeenCalledWith(
        reclaimed,
        { state: 'completed' },
        expect.any(AbortSignal),
      )
      expect(attempts).toEqual([
        { recipient: 'agent-one', generation: original.lease_generation },
        { recipient: 'agent-one', generation: reclaimed.lease_generation },
        { recipient: 'agent-two', generation: reclaimed.lease_generation },
      ])
      expect(inserted).toEqual(['agent-one', 'agent-two'])
      expect(accepted.size).toBe(2)
      expect(restarted.behavior).toHaveBeenCalledOnce()
      expect(restarted.workBudget.usedBytes).toBe(0)
    },
  )

  it.each(['shutdown', 'deadline'] as const)(
    'exhausts the durable attempt budget on final-attempt %s',
    async (interruption) => {
      const { options, controller, client, behavior, workBudget } = fixture()
      client.claimNextEvent.mockResolvedValueOnce({
        ...receipt(),
        attempt_count: options.maxAttempts,
      })
      behavior.mockImplementation((_queued, { signal }) => untilAbort(signal))
      client.completeEvent.mockImplementation(() => {
        controller.abort()
        return Promise.resolve()
      })
      const running = new ReceiptConsumer(options).run(controller.signal)
      await flush()
      if (interruption === 'shutdown') controller.abort()
      else await vi.advanceTimersByTimeAsync(options.behaviorTimeoutMs)
      await running
      expect(client.completeEvent.mock.calls[0]?.[1]).toEqual({
        state: 'failed',
        last_error: { code: 'retry_budget_exhausted' },
      })
      expect(behavior).toHaveBeenCalledOnce()
      expect(client.claimNextEvent).toHaveBeenCalledOnce()
      expect(workBudget.usedBytes).toBe(0)
    },
  )

  it('retries work that crosses the deadline before the abort timer runs', async () => {
    const { options, controller, client, behavior } = fixture()
    client.claimNextEvent.mockResolvedValueOnce(receipt())
    behavior.mockImplementation((_queued, { deadlineMs }) => {
      vi.setSystemTime(deadlineMs)
      return Promise.resolve()
    })
    client.completeEvent.mockImplementation(() => {
      controller.abort()
      return Promise.resolve()
    })
    await new ReceiptConsumer(options).run(controller.signal)
    expect(client.completeEvent.mock.calls[0]?.[1]).toEqual({
      state: 'pending',
      last_error: { code: 'retryable_failure' },
    })
    expect(behavior).toHaveBeenCalledOnce()
  })

  it.each(['claim', 'behavior', 'completion'] as const)(
    'retains admission for an uncooperative %s after deadline and bounded shutdown',
    async (phase) => {
      const { options, controller, client, behavior, workBudget } = fixture()
      const stuck = deferred()
      client.claimNextEvent.mockResolvedValueOnce(receipt())
      if (phase === 'claim')
        client.claimNextEvent
          .mockReset()
          .mockImplementation(() => stuck.promise.then(() => undefined))
      if (phase === 'behavior') behavior.mockImplementation(() => stuck.promise)
      if (phase === 'completion') client.completeEvent.mockImplementation(() => stuck.promise)
      const running = new ReceiptConsumer(options).run(controller.signal)
      await vi.advanceTimersByTimeAsync(400)
      expect(client.claimNextEvent).toHaveBeenCalledOnce()
      expect(workBudget.usedBytes).toBe(initialReceiptWorkBytes)
      controller.abort()
      await running
      expect(workBudget.usedBytes).toBe(initialReceiptWorkBytes)
      stuck.resolve()
      await flush()
      expect(workBudget.usedBytes).toBe(0)
      if (phase === 'claim') expect(behavior).not.toHaveBeenCalled()
    },
  )

  it('aborts actual claim I/O and does not start behavior on cancellation', async () => {
    const { options, controller, client, behavior, workBudget } = fixture()
    let claimSignal: AbortSignal | undefined
    client.claimNextEvent.mockImplementation(async (_capability, _leaseMs, signal) => {
      if (!signal) throw new Error('missing signal')
      claimSignal = signal
      await untilAbort(signal)
      return receipt()
    })
    const running = new ReceiptConsumer(options).run(controller.signal)
    await flush()
    controller.abort()
    await running
    expect(claimSignal?.aborted).toBe(true)
    expect(behavior).not.toHaveBeenCalled()
    expect(client.completeEvent).not.toHaveBeenCalled()
    expect(workBudget.usedBytes).toBe(0)
  })

  it('rejects expired leases before behavior and performs no unscoped completion', async () => {
    const { options, controller, client, behavior, logger } = fixture()
    client.claimNextEvent.mockImplementationOnce(() => {
      controller.abort()
      return Promise.resolve(receipt())
    })
    const running = new ReceiptConsumer(options).run(controller.signal)
    await running
    expect(behavior).not.toHaveBeenCalled()
    expect(client.completeEvent).not.toHaveBeenCalled()
    // Separately exercise a decoded receipt whose lease is already expired.
    const next = fixture()
    next.client.claimNextEvent.mockResolvedValueOnce({
      ...receipt(),
      lease_expires_at: new Date(Date.now() - 1).toISOString(),
    })
    const nextRun = new ReceiptConsumer(next.options).run(next.controller.signal)
    await flush()
    next.controller.abort()
    await nextRun
    expect(next.behavior).not.toHaveBeenCalled()
    expect(next.client.completeEvent).not.toHaveBeenCalled()
    expect(next.logger.error).toHaveBeenCalledWith(
      'channel receipt lease expired before processing',
    )
    expect(logger.error).not.toHaveBeenCalled()
  })

  it('rejects ambiguous capabilities and invalid budgets before any claim', async () => {
    const { options, client, controller } = fixture()
    expect(
      () =>
        new ReceiptConsumer({
          ...options,
          capabilities: [...options.capabilities, ...options.capabilities],
        }),
    ).toThrow('ambiguous')
    for (const invalid of [
      { leaseMs: 999 },
      { completionTimeoutMs: 1000 },
      { maxAttempts: 0 },
      { maxConcurrentEvents: Infinity },
      { workBudget: new WorkByteBudget(1) },
    ]) {
      expect(() => new ReceiptConsumer({ ...options, ...invalid })).toThrow('configuration')
    }
    const consumer = new ReceiptConsumer(options)
    controller.abort()
    await consumer.run(controller.signal)
    await expect(consumer.run(controller.signal)).rejects.toThrow('already started')
    expect(client.claimNextEvent).not.toHaveBeenCalled()
  })
})

function fixture() {
  const controller = new AbortController()
  const client = {
    claimNextEvent: vi
      .fn<ReceiptConsumerOptions['client']['claimNextEvent']>()
      .mockResolvedValue(undefined),
    completeEvent: vi.fn<ReceiptConsumerOptions['client']['completeEvent']>().mockResolvedValue(),
  }
  const behavior = vi.fn<ReceiptConsumerOptions['behavior']>().mockResolvedValue()
  const workBudget = new WorkByteBudget(initialReceiptWorkBytes * 2)
  const logger = { error: vi.fn() }
  const options: ReceiptConsumerOptions = {
    capabilities: [{ connector_key: 'custom', provider: 'slack' }],
    client,
    behavior,
    workBudget,
    maxConcurrentEvents: 1,
    maxAttempts: 3,
    leaseMs: 1000,
    claimTimeoutMs: 100,
    behaviorTimeoutMs: 200,
    completionTimeoutMs: 100,
    idlePollMs: 10,
    logger,
    random: () => 0,
  }
  return { options, controller, client, behavior, workBudget, logger }
}

function receipt(index = 1): ChannelConnectorEventReceipt {
  return {
    receipt_id: `irec_${String(index).padStart(26, 'a')}`,
    integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    integration_install_id: 'iin_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    event_id: `provider-event-${index}`,
    state: 'processing',
    attempt_count: 1,
    lease_token: '01994550-1234-7123-8123-123456789abc',
    lease_generation: 3,
    lease_expires_at: new Date(Date.now() + 1000).toISOString(),
    last_error: {},
    payload: { lease_token: 'payload-is-not-authority', text: 'verified event' },
  }
}

function deferred() {
  let resolve!: () => void
  const promise = new Promise<void>((settle) => {
    resolve = settle
  })
  return { promise, resolve }
}

function untilAbort(signal: AbortSignal): Promise<never> {
  return new Promise((_resolve, reject) => {
    if (signal.aborted) reject(new Error('aborted'))
    else
      signal.addEventListener(
        'abort',
        () => {
          reject(new Error('aborted'))
        },
        { once: true },
      )
  })
}

async function flush(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0)
}
