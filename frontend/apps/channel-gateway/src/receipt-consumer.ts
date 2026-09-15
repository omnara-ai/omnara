import {
  type ChannelConnectorCapability,
  type ChannelConnectorEventReceipt,
  schemas,
} from '@omnara/sdk'

import { abortableDelay, pollJitterMilliseconds, raceWithAbort } from './async'
import type { CoreClient, ReceiptCompletion } from './core-client'
import { initialReceiptWorkBytes } from './receipt-http'
import { type GatewayLogger, type ReceiptBehavior, ReceiptBehaviorError } from './types'
import type { WorkByteBudget } from './work-budget'

export type { ReceiptBehavior, ReceiptBehaviorContext } from './types'

export interface ReceiptConsumerOptions {
  capabilities: readonly ChannelConnectorCapability[]
  client: Pick<CoreClient, 'claimNextEvent' | 'completeEvent'>
  behavior: ReceiptBehavior
  workBudget: WorkByteBudget
  maxConcurrentEvents: number
  /** Includes prior claims/restarts via receipt.attempt_count, not a local counter. */
  maxAttempts: number
  leaseMs: number
  claimTimeoutMs: number
  behaviorTimeoutMs: number
  completionTimeoutMs: number
  idlePollMs: number
  logger: Pick<GatewayLogger, 'error'>
  random?: () => number
}

/** One leased receipt per free worker. Core owns durable retry/backoff and its
 * outer attempt limit; this consumer terminalizes exhausted/permanent failures.
 * There is no connection to handleWebhook or the outgoing delivery loop.
 */
export class ReceiptConsumer {
  private readonly capabilities: readonly ChannelConnectorCapability[]
  private cursor = 0
  private started = false

  constructor(private readonly options: ReceiptConsumerOptions) {
    if (
      ![
        options.maxConcurrentEvents,
        options.maxAttempts,
        options.claimTimeoutMs,
        options.behaviorTimeoutMs,
        options.completionTimeoutMs,
        options.idlePollMs,
      ].every((value) => Number.isSafeInteger(value) && value > 0 && value <= 2_147_483_647) ||
      options.completionTimeoutMs >= options.leaseMs ||
      options.workBudget.limitBytes < initialReceiptWorkBytes ||
      options.capabilities.length === 0 ||
      options.capabilities.length > 64
    )
      throw new Error('invalid channel receipt consumer configuration')
    const seen = new Set<string>()
    this.capabilities = options.capabilities.map((capability) => {
      if (
        !schemas.zClaimNextChannelConnectorEventRequest.safeParse({
          capability,
          lease_ms: options.leaseMs,
        }).success
      ) {
        throw new Error('invalid channel receipt consumer configuration')
      }
      const key = `${capability.connector_key}\u0000${capability.provider}`
      if (seen.has(key)) throw new Error('ambiguous channel receipt capability')
      seen.add(key)
      return Object.freeze({ ...capability })
    })
  }

  /** One lifecycle per consumer. Cancellation stops admission and actual I/O.
   * Uncooperative callbacks keep their slot/memory until they settle; shutdown
   * waits only for bounded completion attempts, not those callbacks forever.
   */
  async run(signal: AbortSignal): Promise<void> {
    if (this.started) throw new Error('channel receipt consumer already started')
    this.started = true
    const workers = Math.min(
      this.options.maxConcurrentEvents,
      Math.floor(this.options.workBudget.limitBytes / initialReceiptWorkBytes),
    )
    await Promise.all(Array.from({ length: workers }, () => this.worker(signal)))
  }

  private async worker(shutdown: AbortSignal): Promise<void> {
    const isStopping = (): boolean => shutdown.aborted
    while (!isStopping()) {
      let reservation
      try {
        reservation = this.options.workBudget.reserve(initialReceiptWorkBytes)
      } catch {
        await this.poll(shutdown)
        continue
      }
      const unfinished = new Set<Promise<unknown>>()
      let empty = false
      try {
        const capability = this.capabilities[this.cursor]
        this.cursor = (this.cursor + 1) % this.capabilities.length
        if (!capability) return
        const claimStartedAt = Date.now()
        const receipt = await withinDeadline(
          claimStartedAt + this.options.claimTimeoutMs,
          shutdown,
          (signal) =>
            this.options.client.claimNextEvent(
              capability,
              this.options.leaseMs,
              signal,
              reservation,
            ),
          unfinished,
        )
        if (!receipt) {
          empty = true
        } else {
          await this.process(Object.freeze({ ...receipt }), claimStartedAt, shutdown, unfinished)
        }
      } catch {
        if (!shutdown.aborted) this.options.logger.error('channel receipt claim failed')
        empty = true
      } finally {
        // A timed-out job may still retain the decoded receipt. Do not recycle
        // its memory or worker for another claim until actual work settles.
        const released = Promise.allSettled(unfinished).then(() => {
          reservation.release()
        })
        try {
          await raceWithAbort(released, shutdown)
        } catch {
          /* Release is observed above even after shutdown. */
        }
      }
      if (empty && !shutdown.aborted) await this.poll(shutdown)
    }
  }

  private async process(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    claimStartedAt: number,
    shutdown: AbortSignal,
    unfinished: Set<Promise<unknown>>,
  ): Promise<void> {
    const leaseDeadlineMs = Math.min(
      claimStartedAt + this.options.leaseMs,
      Date.parse(receipt.lease_expires_at),
    )
    if (!Number.isFinite(leaseDeadlineMs) || leaseDeadlineMs <= Date.now()) {
      this.options.logger.error('channel receipt lease expired before processing')
      return
    }
    const deadlineMs = Math.min(
      Date.now() + this.options.behaviorTimeoutMs,
      leaseDeadlineMs - this.options.completionTimeoutMs,
    )
    let started = false
    let completion: ReceiptCompletion
    try {
      if (receipt.attempt_count > this.options.maxAttempts) throw new ReceiptBehaviorError(false)
      await withinDeadline(
        deadlineMs,
        shutdown,
        (signal) => {
          started = true
          return this.options.behavior(receipt, { deadlineMs, signal })
        },
        unfinished,
      )
      completion = { state: 'completed' }
    } catch (cause) {
      const safeRetry = cause instanceof ReceiptBehaviorError ? cause.retryable : !started
      const retry = safeRetry && receipt.attempt_count < this.options.maxAttempts
      completion = {
        state: retry ? 'pending' : 'failed',
        last_error: {
          code: safeRetry
            ? retry
              ? 'retryable_failure'
              : 'retry_budget_exhausted'
            : cause instanceof ReceiptBehaviorError
              ? 'permanent_failure'
              : 'behavior_outcome_unknown',
        },
      }
    }
    try {
      // Shutdown cancellation does not prevent a final bounded fenced report.
      await withinDeadline(
        Math.min(leaseDeadlineMs, Date.now() + this.options.completionTimeoutMs),
        undefined,
        (signal) => this.options.client.completeEvent(receipt, completion, signal),
        unfinished,
      )
    } catch {
      // An ambiguous completion can only be recovered by core's receipt lifecycle;
      // do not rerun behavior or turn it into a second completion/send loop.
      this.options.logger.error('channel receipt completion failed', {
        receipt_id: receipt.receipt_id,
        state: completion.state,
      })
    }
  }

  private poll(signal: AbortSignal): Promise<boolean> {
    return abortableDelay(
      pollJitterMilliseconds(this.options.idlePollMs, this.options.random),
      signal,
    )
  }
}

async function withinDeadline<T>(
  deadlineMs: number,
  parent: AbortSignal | undefined,
  operation: (signal: AbortSignal) => Promise<T>,
  unfinished: Set<Promise<unknown>>,
): Promise<T> {
  const controller = new AbortController()
  const abort = (): void => {
    controller.abort()
  }
  const remaining = deadlineMs - Date.now()
  if (parent?.aborted || remaining <= 0) controller.abort()
  parent?.addEventListener('abort', abort, { once: true })
  const timer = setTimeout(abort, Math.max(1, remaining))
  try {
    controller.signal.throwIfAborted()
    const work = Promise.resolve().then(() => {
      controller.signal.throwIfAborted()
      return operation(controller.signal)
    })
    unfinished.add(work)
    void work.then(
      () => unfinished.delete(work),
      () => unfinished.delete(work),
    )
    const result = await raceWithAbort(work, controller.signal)
    controller.signal.throwIfAborted()
    if (Date.now() >= deadlineMs) throw new Error('channel receipt deadline exceeded')
    return result
  } finally {
    clearTimeout(timer)
    parent?.removeEventListener('abort', abort)
    controller.abort()
  }
}
