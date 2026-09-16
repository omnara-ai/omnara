import { type ChannelConnectorEventReceipt, schemas } from '@omnara/sdk'

import type { CoreClient, ReceiptCompletion } from './core-client'
import {
  LeasedReceiptConsumer,
  type LeasedReceiptConsumerSettings,
} from './leased-receipt-consumer'
import { type ReceiptBehavior, ReceiptBehaviorError } from './types'

export type { ReceiptBehavior, ReceiptBehaviorContext } from './types'

export interface ReceiptConsumerOptions extends LeasedReceiptConsumerSettings {
  client: Pick<CoreClient, 'claimNextEvent' | 'completeEvent'>
  behavior: ReceiptBehavior
  maxConcurrentEvents: number
  /** Includes prior claims/restarts via receipt.attempt_count, not a local counter. */
  maxAttempts: number
}

/** Message receipts retain their existing bounded attempt and completion contract. */
export class ReceiptConsumer extends LeasedReceiptConsumer<
  ChannelConnectorEventReceipt,
  ReceiptCompletion
> {
  constructor(options: ReceiptConsumerOptions) {
    if (
      !Number.isSafeInteger(options.maxAttempts) ||
      options.maxAttempts <= 0 ||
      options.maxAttempts > 2_147_483_647
    )
      throw new Error('invalid channel receipt consumer configuration')
    super({
      ...options,
      maxConcurrentReceipts: options.maxConcurrentEvents,
      validateClaim: (capability, leaseMs) =>
        schemas.zClaimNextChannelConnectorEventRequest.safeParse({ capability, lease_ms: leaseMs })
          .success,
      claim: (...args) => options.client.claimNextEvent(...args),
      complete: (...args) => options.client.completeEvent(...args),
      completionState: (completion) => completion.state,
      behavior: async (receipt, { deadlineMs, signal }) => {
        if (receipt.attempt_count > options.maxAttempts) throw new ReceiptBehaviorError(false)
        await options.behavior(receipt, { deadlineMs, signal })
        return { state: 'completed' }
      },
      classifyFailure: (receipt, cause, started) => {
        const safeRetry = cause instanceof ReceiptBehaviorError ? cause.retryable : !started
        const retry = safeRetry && receipt.attempt_count < options.maxAttempts
        const completion: ReceiptCompletion = {
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
        const retryAfterMs = cause instanceof ReceiptBehaviorError ? cause.retryAfterMs : undefined
        // Never drop or shorten a supplied provider minimum and retry too soon.
        // The generated contract owns the bounds; do not coerce strings/fractions.
        if (retry && retryAfterMs !== undefined) {
          if (
            !Number.isSafeInteger(retryAfterMs) ||
            !schemas.zCompleteChannelConnectorEventRequest.shape.retry_after_ms.safeParse(
              retryAfterMs,
            ).success
          ) {
            return { state: 'failed', last_error: { code: 'invalid_retry_hint' } }
          }
          completion.retry_after_ms = retryAfterMs
        }
        return completion
      },
    })
  }
}
