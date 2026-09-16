import {
  type ChannelConnectorCapability,
  type ChannelConnectorControlReceipt,
  type ChannelInboundControlEventRequest,
  type ChannelInboundControlEventResponse,
  type createOmnaraClient,
  schemas,
  sdk,
} from '@omnara/sdk'

import type { ControlCompletion, ProviderWorkReservation } from '../types'
import { ReceiptClientError, receiptFetch } from './receipt-http'
import { receiptFailure } from './requests'

type Client = ReturnType<typeof createOmnaraClient>

/** Save verified finite control work before acknowledging its provider delivery. */
export async function submitControlEvent(
  client: Client,
  transport: typeof fetch,
  appId: string,
  event: ChannelInboundControlEventRequest,
  signal: AbortSignal,
): Promise<ChannelInboundControlEventResponse> {
  try {
    signal.throwIfAborted()
    const { data, response } = await sdk.acceptChannelConnectorControlEvent({
      client,
      path: { integrationAppID: appId },
      body: event,
      signal,
      redirect: 'error',
      fetch: receiptFetch(transport, 4096, signal),
    })
    if (
      response.status !== 202 ||
      !schemas.zChannelInboundControlEventResponse.safeParse(data).success
    )
      throw new ReceiptClientError('invalid_response')
    return data
  } catch (cause) {
    throw receiptFailure(cause, signal)
  }
}

/** One claim request: an ambiguous response leaves recovery to the original lease. */
export async function claimNextControlEvent(
  client: Client,
  transport: typeof fetch,
  capability: ChannelConnectorCapability,
  leaseMs: number,
  signal: AbortSignal,
  work?: ProviderWorkReservation,
): Promise<ChannelConnectorControlReceipt | undefined> {
  try {
    signal.throwIfAborted()
    const { data, response } = await sdk.claimNextChannelConnectorControlEvent({
      client,
      body: { capability, lease_ms: leaseMs },
      signal,
      redirect: 'error',
      fetch: receiptFetch(transport, 96 * 1024, signal, work),
    })
    if (response.status === 204) return undefined
    if (
      response.status !== 200 ||
      !data ||
      !schemas.zChannelConnectorControlReceipt.safeParse(data).success ||
      data.state !== 'processing' ||
      !Number.isSafeInteger(data.lease_generation) ||
      !Number.isSafeInteger(data.attempts_since_progress) ||
      (data.last_installation_id !== null && data.end_installation_id === null)
    )
      throw new ReceiptClientError('invalid_response')
    return data
  } catch (cause) {
    throw receiptFailure(cause, signal)
  }
}

/** Report against the original app and lease. Never retry behavior or refresh
 * a lease after an ambiguous completion; the next claim re-observes state.
 */
export async function completeControlEvent(
  client: Client,
  transport: typeof fetch,
  receipt: Readonly<ChannelConnectorControlReceipt>,
  completion: ControlCompletion,
  signal: AbortSignal,
): Promise<void> {
  try {
    signal.throwIfAborted()
    const { data, response } = await sdk.completeChannelConnectorControlEvent({
      client,
      path: { integrationAppID: receipt.integration_app_id, receiptID: receipt.receipt_id },
      body: {
        ...completion,
        lease_token: receipt.lease_token,
        lease_generation: receipt.lease_generation,
      },
      signal,
      redirect: 'error',
      fetch: receiptFetch(transport, 4096, signal),
    })
    const state =
      completion.outcome === 'yield' || completion.outcome === 'retry'
        ? 'pending'
        : completion.outcome
    if (
      response.status !== 200 ||
      !schemas.zChannelInboundControlEventResponse.safeParse(data).success ||
      data.receipt_id !== receipt.receipt_id ||
      data.state !== state ||
      data.end_installation_id !== receipt.end_installation_id ||
      data.last_installation_id !==
        (completion.last_installation_id ?? receipt.last_installation_id)
    )
      throw new ReceiptClientError('invalid_response')
  } catch (cause) {
    throw receiptFailure(cause, signal)
  }
}
