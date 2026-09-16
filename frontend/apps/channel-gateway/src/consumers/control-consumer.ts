import { type ChannelConnectorControlReceipt, schemas } from '@omnara/sdk'

import type { CoreClient } from '../core/client'
import { type ControlCompletion, type ControlReceiptBehavior, ReceiptBehaviorError } from '../types'
import { LeasedReceiptConsumer, type LeasedReceiptConsumerSettings } from './leased-consumer'

export interface ControlReceiptConsumerOptions extends LeasedReceiptConsumerSettings {
  client: Pick<CoreClient, 'claimNextControlEvent' | 'completeControlEvent'>
  behavior: ControlReceiptBehavior
}

/** One worker for replayable provider-state observations. Core applies backoff
 * using attempts_since_progress; successful progress may yield indefinitely.
 * Only an explicit permanent classification terminalizes bad immutable work.
 */
export class ControlReceiptConsumer extends LeasedReceiptConsumer<
  ChannelConnectorControlReceipt,
  ControlCompletion
> {
  constructor(options: ControlReceiptConsumerOptions) {
    super({
      ...options,
      maxConcurrentReceipts: 1,
      validateClaim: (capability, leaseMs) =>
        schemas.zClaimNextChannelConnectorControlEventRequest.safeParse({
          capability,
          lease_ms: leaseMs,
        }).success,
      claim: (...args) => options.client.claimNextControlEvent(...args),
      complete: (...args) => options.client.completeControlEvent(...args),
      completionState: (completion) => completion.outcome,
      classifyFailure: (_receipt, cause) => {
        const permanent = cause instanceof ReceiptBehaviorError && !cause.retryable
        return {
          outcome: permanent ? 'failed' : 'retry',
          last_error: { code: permanent ? 'permanent_failure' : 'retryable_failure' },
        }
      },
    })
  }
}
