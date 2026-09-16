import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ControlReceiptConsumer, type ControlReceiptConsumerOptions } from './control-consumer'
import { controlCapability, controlReceipt, nextInstallationId } from './control-test-support'
import type { ControlCompletion } from './core-controls'
import { initialReceiptWorkBytes } from './receipt-http'
import { type ControlReceiptBehaviorContext, ReceiptBehaviorError } from './types'
import { GatewayAtCapacityError, WorkByteBudget } from './work-budget'

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-09-15T12:00:00Z'))
})
afterEach(() => {
  vi.useRealTimers()
})

describe('control receipt consumer', () => {
  const completions: ControlCompletion[] = [
    { outcome: 'completed' },
    { outcome: 'yield', last_installation_id: nextInstallationId },
    {
      outcome: 'retry',
      last_installation_id: nextInstallationId,
      retry_after_ms: 60_000,
      last_error: { code: 'provider_rate_limited' },
    },
    { outcome: 'failed', last_error: { code: 'malformed_control' } },
  ]
  it.each(completions)(
    'preserves the typed $outcome result and original app receipt proof',
    async (completion) => {
      const f = fixture()
      const receipt = controlReceipt()
      f.client.claimNextControlEvent.mockResolvedValueOnce(receipt)
      f.behavior.mockResolvedValue(completion)
      f.client.completeControlEvent.mockImplementation(() => {
        f.controller.abort()
        return Promise.resolve()
      })
      await new ControlReceiptConsumer(f.options).run(f.controller.signal)
      const delivered = f.behavior.mock.calls[0]?.[0]
      expect(delivered).toEqual(receipt)
      expect(Object.isFrozen(delivered)).toBe(true)
      expect(f.client.completeControlEvent).toHaveBeenCalledExactlyOnceWith(
        delivered,
        completion,
        expect.any(AbortSignal),
      )
      expect(f.client.completeControlEvent.mock.calls[0]?.[0]).toBe(delivered)
      expect(f.client.completeControlEvent.mock.calls[0]?.[1]).toBe(completion)
      expect(f.client.claimNextControlEvent).toHaveBeenCalledOnce()
      expect(f.budget.usedBytes).toBe(0)
    },
  )

  it.each([
    { cause: new Error('private provider failure'), outcome: 'retry' },
    { cause: new DOMException('aborted', 'AbortError'), outcome: 'retry' },
    { cause: new ReceiptBehaviorError(true), outcome: 'retry' },
    { cause: new ReceiptBehaviorError(false), outcome: 'failed' },
  ])(
    'classifies $outcome without a total claim cap or leaking errors',
    async ({ cause, outcome }) => {
      const f = fixture()
      f.client.claimNextControlEvent.mockResolvedValueOnce({
        ...controlReceipt(),
        attempts_since_progress: 100_000,
      })
      f.behavior.mockRejectedValue(cause)
      f.client.completeControlEvent.mockImplementation(() => {
        f.controller.abort()
        return Promise.resolve()
      })
      await new ControlReceiptConsumer(f.options).run(f.controller.signal)
      expect(f.behavior).toHaveBeenCalledOnce()
      expect(f.client.completeControlEvent.mock.calls[0]?.[1]).toEqual({
        outcome,
        last_error: { code: outcome === 'retry' ? 'retryable_failure' : 'permanent_failure' },
      })
    },
  )

  it('restarts from core-confirmed progress after yield with no local progress or attempt ceiling', async () => {
    const first = fixture()
    const original = { ...controlReceipt(), attempts_since_progress: 99_999 }
    first.client.claimNextControlEvent.mockResolvedValueOnce(original)
    first.behavior.mockResolvedValue({ outcome: 'yield', last_installation_id: nextInstallationId })
    first.client.completeControlEvent.mockImplementation(() => {
      first.controller.abort()
      return Promise.resolve()
    })
    await new ControlReceiptConsumer(first.options).run(first.controller.signal)

    const next = fixture()
    const reclaimed = {
      ...original,
      lease_generation: original.lease_generation + 1,
      lease_token: '01994550-1234-7123-8123-123456789abd',
      last_installation_id: nextInstallationId,
      attempts_since_progress: 1,
    }
    next.client.claimNextControlEvent.mockResolvedValueOnce(reclaimed)
    next.client.completeControlEvent.mockImplementation(() => {
      next.controller.abort()
      return Promise.resolve()
    })
    await new ControlReceiptConsumer(next.options).run(next.controller.signal)
    expect(next.behavior.mock.calls[0]?.[0]).toEqual(reclaimed)
    expect(next.behavior.mock.calls[0]?.[0].payload).toBe(original.payload)
    expect(next.client.completeControlEvent).toHaveBeenCalledExactlyOnceWith(
      reclaimed,
      { outcome: 'completed' },
      expect.any(AbortSignal),
    )
  })

  it.each(['shutdown', 'deadline'] as const)(
    'retries interrupted observations on %s with one final bounded completion',
    async (interruption) => {
      const f = fixture()
      f.client.claimNextControlEvent.mockResolvedValueOnce(controlReceipt())
      let context: ControlReceiptBehaviorContext | undefined
      f.behavior.mockImplementation((_receipt, incoming) => {
        context = incoming
        incoming.reserveWorkBytes(1_024)
        return new Promise((_resolve, reject) => {
          incoming.signal.addEventListener(
            'abort',
            () => {
              reject(new Error('interrupted'))
            },
            {
              once: true,
            },
          )
        })
      })
      f.client.completeControlEvent.mockImplementation((_receipt, _completion, signal) => {
        expect(signal.aborted).toBe(false)
        f.controller.abort()
        return Promise.resolve()
      })
      const running = new ControlReceiptConsumer(f.options).run(f.controller.signal)
      await vi.advanceTimersByTimeAsync(0)
      if (interruption === 'shutdown') f.controller.abort()
      else await vi.advanceTimersByTimeAsync(f.options.behaviorTimeoutMs)
      await running
      expect(context?.signal.aborted).toBe(true)
      expect(f.client.completeControlEvent).toHaveBeenCalledOnce()
      expect(f.client.completeControlEvent.mock.calls[0]?.[1]).toEqual({
        outcome: 'retry',
        last_error: { code: 'retryable_failure' },
      })
      expect(f.budget.usedBytes).toBe(0)
    },
  )

  it('holds one saturated half-global control slot after timeout/shutdown until actual work settles', async () => {
    const f = fixture()
    const parent = new WorkByteBudget(256 * 1024 * 1024)
    const child = new WorkByteBudget(parent.limitBytes / 2, parent)
    const retainedBytes = child.limitBytes - initialReceiptWorkBytes
    f.options.workBudget = child
    f.client.claimNextControlEvent.mockResolvedValueOnce(controlReceipt())
    let settle!: (completion: ControlCompletion) => void
    let context: ControlReceiptBehaviorContext | undefined
    f.behavior.mockImplementation((_receipt, incoming) => {
      context = incoming
      incoming.reserveWorkBytes(retainedBytes)
      return new Promise((resolve) => {
        settle = resolve
      })
    })
    const running = new ControlReceiptConsumer(f.options).run(f.controller.signal)
    await vi.advanceTimersByTimeAsync(500)
    expect(f.client.claimNextControlEvent).toHaveBeenCalledOnce()
    expect(f.client.completeControlEvent).toHaveBeenCalledOnce()
    expect([child.usedBytes, parent.usedBytes]).toEqual([child.limitBytes, child.limitBytes])
    expect(() => context?.reserveWorkBytes(1)).toThrow(GatewayAtCapacityError)
    // Small ingress still admits even with the entire control residency retained.
    const ingress = parent.reserve(64 * 1024)
    ingress.release()
    f.controller.abort()
    await running
    expect(child.usedBytes).toBe(child.limitBytes)
    settle({ outcome: 'completed' })
    await vi.advanceTimersByTimeAsync(0)
    expect([child.usedBytes, parent.usedBytes]).toEqual([0, 0])
    expect(() => context?.reserveWorkBytes(1)).toThrow('closed')
    expect(f.client.completeControlEvent).toHaveBeenCalledOnce()
  })

  it('does not repeat behavior/completion or invent progress after an ambiguous completion', async () => {
    const f = fixture()
    f.client.claimNextControlEvent.mockResolvedValueOnce(controlReceipt())
    f.behavior.mockResolvedValue({ outcome: 'yield', last_installation_id: nextInstallationId })
    f.client.completeControlEvent.mockImplementation(() => {
      f.controller.abort()
      return Promise.reject(new Error('private lost response'))
    })
    await new ControlReceiptConsumer(f.options).run(f.controller.signal)
    expect(f.behavior).toHaveBeenCalledOnce()
    expect(f.client.completeControlEvent).toHaveBeenCalledOnce()
    expect(f.logger.error).toHaveBeenCalledWith('channel receipt completion failed', {
      receipt_id: controlReceipt().receipt_id,
      state: 'yield',
    })
  })

  it('rejects expired authority before behavior/completion and stops an idle worker', async () => {
    const f = fixture()
    f.client.claimNextControlEvent.mockResolvedValueOnce({
      ...controlReceipt(),
      lease_expires_at: new Date(Date.now() - 1).toISOString(),
    })
    const consumer = new ControlReceiptConsumer(f.options)
    const running = consumer.run(f.controller.signal)
    await vi.advanceTimersByTimeAsync(0)
    f.controller.abort()
    await running
    expect(f.behavior).not.toHaveBeenCalled()
    expect(f.client.completeControlEvent).not.toHaveBeenCalled()
    await expect(consumer.run(f.controller.signal)).rejects.toThrow('already started')
  })

  it('waits before claiming when unrelated global work fills the parent budget', async () => {
    const f = fixture()
    const parent = new WorkByteBudget(4 * initialReceiptWorkBytes)
    const child = new WorkByteBudget(parent.limitBytes / 2, parent)
    f.options.workBudget = child
    const ingress = parent.reserve(parent.limitBytes)
    f.client.claimNextControlEvent.mockResolvedValueOnce(controlReceipt())
    f.client.completeControlEvent.mockImplementation(() => {
      f.controller.abort()
      return Promise.resolve()
    })
    const running = new ControlReceiptConsumer(f.options).run(f.controller.signal)
    await vi.advanceTimersByTimeAsync(30)
    expect(f.client.claimNextControlEvent).not.toHaveBeenCalled()
    expect(child.usedBytes).toBe(0)
    ingress.release()
    await vi.advanceTimersByTimeAsync(10)
    await running
    expect(f.client.claimNextControlEvent).toHaveBeenCalledOnce()
    expect([child.usedBytes, parent.usedBytes]).toEqual([0, 0])
  })
})

function fixture() {
  const controller = new AbortController()
  const client = {
    claimNextControlEvent: vi
      .fn<ControlReceiptConsumerOptions['client']['claimNextControlEvent']>()
      .mockResolvedValue(undefined),
    completeControlEvent: vi
      .fn<ControlReceiptConsumerOptions['client']['completeControlEvent']>()
      .mockResolvedValue(),
  }
  const behavior = vi
    .fn<ControlReceiptConsumerOptions['behavior']>()
    .mockResolvedValue({ outcome: 'completed' })
  const budget = new WorkByteBudget(2 * initialReceiptWorkBytes)
  const logger = { error: vi.fn() }
  const options: ControlReceiptConsumerOptions = {
    capabilities: [controlCapability],
    client,
    behavior,
    workBudget: budget,
    leaseMs: 1_000,
    claimTimeoutMs: 100,
    behaviorTimeoutMs: 200,
    completionTimeoutMs: 100,
    idlePollMs: 10,
    logger,
    random: () => 0,
  }
  return { options, client, behavior, budget, controller, logger }
}
