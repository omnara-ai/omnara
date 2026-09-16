import type { ChannelConnectorCapability } from '@omnara/sdk'

import { abortableDelay, pollJitterMilliseconds, raceWithAbort } from './async'
import { initialReceiptWorkBytes } from './receipt-http'
import {
  type ControlReceiptBehaviorContext,
  type GatewayLogger,
  type ProviderWorkReservation,
  ReceiptBehaviorError,
} from './types'
import { type WorkByteBudget, WorkReservationScope } from './work-budget'

export interface LeasedReceiptConsumerSettings {
  capabilities: readonly ChannelConnectorCapability[]
  workBudget: WorkByteBudget
  leaseMs: number
  claimTimeoutMs: number
  behaviorTimeoutMs: number
  completionTimeoutMs: number
  idlePollMs: number
  logger: Pick<GatewayLogger, 'error'>
  random?: () => number
}

interface LeasedReceipt {
  receipt_id: string
  lease_expires_at: string
}

interface LeasedReceiptConsumerOptions<
  R extends LeasedReceipt,
  C,
> extends LeasedReceiptConsumerSettings {
  maxConcurrentReceipts: number
  validateClaim: (capability: ChannelConnectorCapability, leaseMs: number) => boolean
  claim: (
    capability: ChannelConnectorCapability,
    leaseMs: number,
    signal: AbortSignal,
    work: ProviderWorkReservation,
  ) => Promise<R | undefined>
  behavior: (receipt: Readonly<R>, context: ControlReceiptBehaviorContext) => Promise<C>
  classifyFailure: (receipt: Readonly<R>, cause: unknown, started: boolean) => C
  complete: (receipt: Readonly<R>, completion: C, signal: AbortSignal) => Promise<void>
  completionState: (completion: C) => string
}

/** Shared lease/memory lifetime for the two typed incoming receipt consumers.
 * Their wrappers own behavior results and failure/attempt policy; core owns
 * durable scheduling. There is no outgoing send or provider state logic here.
 */
export class LeasedReceiptConsumer<R extends LeasedReceipt, C> {
  private readonly capabilities: readonly ChannelConnectorCapability[]
  private cursor = 0
  private started = false

  constructor(private readonly options: LeasedReceiptConsumerOptions<R, C>) {
    if (
      ![
        options.maxConcurrentReceipts,
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
      if (!options.validateClaim(capability, options.leaseMs)) {
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
      this.options.maxConcurrentReceipts,
      Math.floor(this.options.workBudget.limitBytes / initialReceiptWorkBytes),
    )
    await Promise.all(Array.from({ length: workers }, () => this.worker(signal)))
  }

  private async worker(shutdown: AbortSignal): Promise<void> {
    const isStopping = (): boolean => shutdown.aborted
    while (!isStopping()) {
      const work = new WorkReservationScope(this.options.workBudget.reserve)
      let reservation
      try {
        reservation = work.reserve(initialReceiptWorkBytes)
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
          (signal) => this.options.claim(capability, this.options.leaseMs, signal, reservation),
          unfinished,
        )
        if (!receipt) {
          empty = true
        } else {
          await this.process(
            Object.freeze({ ...receipt }),
            claimStartedAt,
            shutdown,
            unfinished,
            work,
          )
        }
      } catch {
        if (!shutdown.aborted) this.options.logger.error('channel receipt claim failed')
        empty = true
      } finally {
        // A timed-out job may still retain the decoded receipt. Do not recycle
        // its memory or worker for another claim until actual work settles.
        const released = Promise.allSettled(unfinished).then(() => {
          work.close()
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
    receipt: Readonly<R>,
    claimStartedAt: number,
    shutdown: AbortSignal,
    unfinished: Set<Promise<unknown>>,
    work: WorkReservationScope,
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
    let completion: C
    try {
      completion = await withinDeadline(
        deadlineMs,
        shutdown,
        (signal) => {
          started = true
          return this.options.behavior(receipt, {
            deadlineMs,
            signal,
            reserveWorkBytes: work.reserve,
          })
        },
        unfinished,
      )
    } catch (cause) {
      completion = this.options.classifyFailure(receipt, cause, started)
    }
    try {
      // Shutdown cancellation does not prevent a final bounded fenced report.
      await withinDeadline(
        Math.min(leaseDeadlineMs, Date.now() + this.options.completionTimeoutMs),
        undefined,
        (signal) => this.options.complete(receipt, completion, signal),
        unfinished,
      )
    } catch {
      // An ambiguous completion can only be recovered by core's receipt lifecycle;
      // do not rerun behavior or turn it into a second completion/send loop.
      this.options.logger.error('channel receipt completion failed', {
        receipt_id: receipt.receipt_id,
        state: this.options.completionState(completion),
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
    // This boundary can win the race against a callback's classified rejection.
    // Interrupted inbox work is replayable, just as after a lost receipt lease.
    controller.abort(new ReceiptBehaviorError(true))
  }
  const remaining = deadlineMs - Date.now()
  if (parent?.aborted || remaining <= 0) abort()
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
    if (Date.now() >= deadlineMs) throw new ReceiptBehaviorError(true)
    return result
  } finally {
    clearTimeout(timer)
    parent?.removeEventListener('abort', abort)
    controller.abort()
  }
}
