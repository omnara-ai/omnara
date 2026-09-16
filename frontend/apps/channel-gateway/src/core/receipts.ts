import {
  type ChannelConnectorCapability,
  type ChannelConnectorEventReceipt,
  type CompleteChannelConnectorEventRequest,
  type createOmnaraClient,
  schemas,
  sdk,
} from '@omnara/sdk'

import type { ProviderWorkReservation } from '../types'
import { maxReceiptResponseBytes, ReceiptClientError, receiptFetch } from './receipt-http'
import { receiptFailure } from './requests'

type Client = ReturnType<typeof createOmnaraClient>
export type ReceiptCompletion = Pick<
  CompleteChannelConnectorEventRequest,
  'state' | 'last_error' | 'retry_after_ms'
>

/** Exactly one claim POST; a lost response is not permission to claim again. */
export async function claimNextEvent(
  client: Client,
  transport: typeof fetch,
  capability: ChannelConnectorCapability,
  leaseMs: number,
  signal: AbortSignal,
  work?: ProviderWorkReservation,
): Promise<ChannelConnectorEventReceipt | undefined> {
  try {
    const { data, response } = await sdk.claimNextChannelConnectorEvent({
      body: { capability, lease_ms: leaseMs },
      client: client,
      fetch: receiptFetch(transport, maxReceiptResponseBytes, signal, work),
      redirect: 'error',
      signal,
    })
    if (response.status === 204) return undefined
    if (
      response.status !== 200 ||
      !data ||
      !schemas.zChannelConnectorEventReceipt.safeParse(data).success ||
      data.state !== 'processing' ||
      !Number.isSafeInteger(data.lease_generation)
    ) {
      throw new ReceiptClientError('invalid_response')
    }
    return data
  } catch (cause) {
    throw receiptFailure(cause, signal)
  }
}

/** Completion carries only the original scoped receipt lease proof. A lost
 * completion response never causes behavior to run again in this consumer.
 */
export async function completeEvent(
  client: Client,
  transport: typeof fetch,
  receipt: Readonly<ChannelConnectorEventReceipt>,
  completion: ReceiptCompletion,
  signal: AbortSignal,
): Promise<void> {
  try {
    if (
      completion.retry_after_ms !== undefined &&
      (completion.state !== 'pending' ||
        !Number.isSafeInteger(completion.retry_after_ms) ||
        !schemas.zCompleteChannelConnectorEventRequest.shape.retry_after_ms.safeParse(
          completion.retry_after_ms,
        ).success)
    )
      throw new ReceiptClientError('invalid_request')
    const { data, response } = await sdk.completeChannelConnectorEvent({
      body: {
        state: completion.state,
        last_error: completion.last_error,
        retry_after_ms: completion.retry_after_ms,
        lease_token: receipt.lease_token,
        lease_generation: receipt.lease_generation,
      },
      client: client,
      fetch: receiptFetch(transport, 64 * 1024, signal),
      redirect: 'error',
      path: {
        integrationAppID: receipt.integration_app_id,
        integrationInstallID: receipt.integration_install_id,
        receiptID: receipt.receipt_id,
      },
      signal,
    })
    if (
      response.status !== 200 ||
      !schemas.zChannelInboundEventResponse.safeParse(data).success ||
      data.receipt_id !== receipt.receipt_id ||
      data.state !== completion.state
    )
      throw new ReceiptClientError('invalid_response')
  } catch (cause) {
    throw receiptFailure(cause, signal)
  }
}
