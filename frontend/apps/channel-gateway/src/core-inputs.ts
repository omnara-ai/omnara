import {
  type ChannelConnectorEventReceipt,
  type ChannelConnectorInputResponse,
  type ChannelDefinition,
  type ChannelInboundEventRequest,
  type ChannelInboundEventResponse,
  type createOmnaraClient,
  type DeliverChannelConnectorInputRequest,
  type DeliverChannelConnectorWorkflowRequest,
  type ListChannelConnectorRoutesResponse,
  type LookupChannelConnectorRecipientsRequest,
  type LookupChannelConnectorWorkflowRequest,
  type LookupChannelConnectorWorkflowResponse,
  type PublishChannelConnectorDefinitionRequest,
  schemas,
  sdk,
} from '@omnara/sdk'

import { receiptFailure, requireData, retryCoreRequest } from './core-http'
import { maxReceiptResponseBytes, ReceiptClientError, receiptFetch } from './receipt-http'
import type { ProviderWorkReservation } from './types'

export type ReceiptInputRequest = Omit<DeliverChannelConnectorInputRequest, 'receipt'>
export type ReceiptWorkflowRequest = Omit<DeliverChannelConnectorWorkflowRequest, 'receipt'>
export type ReceiptRecipientsRequest = Omit<LookupChannelConnectorRecipientsRequest, 'receipt'>
type Client = ReturnType<typeof createOmnaraClient>

export async function lookupInputRecipients(
  client: Client,
  fetch: typeof globalThis.fetch,
  receipt: Readonly<ChannelConnectorEventReceipt>,
  request: ReceiptRecipientsRequest,
  signal: AbortSignal,
) {
  try {
    const body = { ...request, receipt: receiptProof(receipt, signal) }
    if (!schemas.zLookupChannelConnectorRecipientsRequest.safeParse(body).success)
      throw new ReceiptClientError('invalid_request')
    // Include worst-case JSON escaping of every requested key in each row, plus
    // fixed recipient fields and cursor framing. Ordinary Slack pages stay small.
    const keyBytes = request.input_keys.reduce((bytes, key) => bytes + 6 * key.length + 3, 0)
    const responseBytes = 64 * 1024 + (request.limit ?? 50) * (256 + keyBytes)
    const { data, response } = await sdk.lookupChannelConnectorRecipients({
      client,
      body,
      path: receiptPath(receipt),
      signal,
      redirect: 'error',
      fetch: receiptFetch(fetch, responseBytes, signal),
      responseValidator: (data) =>
        schemas.zLookupChannelConnectorRecipientsResponse.safeParse(data).success
          ? Promise.resolve()
          : Promise.reject(new ReceiptClientError('invalid_response')),
    })
    if (
      response.status !== 200 ||
      data.recipients.length > (request.limit ?? 50) ||
      new Set(data.recipients.map((recipient) => recipient.agent_id)).size !==
        data.recipients.length ||
      data.recipients.some(
        (recipient) =>
          recipient.input_keys.length > request.input_keys.length ||
          recipient.input_keys.some((key) => !request.input_keys.includes(key)),
      ) ||
      (!data.channel_id &&
        (data.parent_channel_id ||
          data.has_receive_binding_history ||
          data.workflow_started ||
          data.next_cursor))
    )
      throw new ReceiptClientError('invalid_response')
    return data
  } catch (cause) {
    throw receiptFailure(cause, signal)
  }
}

export async function lookupWorkflowInput(
  client: Client,
  fetch: typeof globalThis.fetch,
  receipt: Readonly<ChannelConnectorEventReceipt>,
  body: LookupChannelConnectorWorkflowRequest,
  signal: AbortSignal,
): Promise<LookupChannelConnectorWorkflowResponse> {
  try {
    signal.throwIfAborted()
    if (!schemas.zLookupChannelConnectorWorkflowRequest.safeParse(body).success)
      throw new ReceiptClientError('invalid_request')
    const { data, response } = await sdk.lookupChannelConnectorWorkflow({
      body,
      client,
      path: receiptPath(receipt),
      signal,
      redirect: 'error',
      fetch: receiptFetch(fetch, 64 * 1024, signal),
      responseValidator: (data) =>
        schemas.zLookupChannelConnectorWorkflowResponse.safeParse(data).success
          ? Promise.resolve()
          : Promise.reject(new ReceiptClientError('invalid_response')),
    })
    if (response.status !== 200 || data.input_keys.some((key) => !body.input_keys.includes(key)))
      throw new ReceiptClientError('invalid_response')
    if (
      data.exists !== (data.agent_state !== undefined) ||
      (!data.exists && data.input_keys.length)
    )
      throw new ReceiptClientError('invalid_response')
    return data
  } catch (cause) {
    throw receiptFailure(cause, signal)
  }
}

export async function deliverBoundInput(
  client: Client,
  fetch: typeof globalThis.fetch,
  receipt: Readonly<ChannelConnectorEventReceipt>,
  request: ReceiptInputRequest,
  signal: AbortSignal,
): Promise<ChannelConnectorInputResponse> {
  try {
    const result = await sdk.deliverChannelConnectorInput(
      inputOptions(client, fetch, receipt, request, signal),
    )
    if (result.response.status !== 200) throw new ReceiptClientError('invalid_response')
    return result.data
  } catch (cause) {
    throw receiptFailure(cause, signal)
  }
}

export async function deliverWorkflowInput(
  client: Client,
  fetch: typeof globalThis.fetch,
  receipt: Readonly<ChannelConnectorEventReceipt>,
  request: ReceiptWorkflowRequest,
  signal: AbortSignal,
): Promise<ChannelConnectorInputResponse> {
  try {
    const result = await sdk.deliverChannelConnectorWorkflow(
      inputOptions(client, fetch, receipt, request, signal),
    )
    if (result.response.status !== 200) throw new ReceiptClientError('invalid_response')
    return result.data
  } catch (cause) {
    throw receiptFailure(cause, signal)
  }
}

/** Both atomic admission paths use the original receipt proof, the same media
 * budget and one POST. Behavior must reselect/rerender after a known conflict.
 */
function inputOptions<T extends ReceiptInputRequest | ReceiptWorkflowRequest>(
  client: Client,
  fetch: typeof globalThis.fetch,
  receipt: Readonly<ChannelConnectorEventReceipt>,
  request: T,
  signal: AbortSignal,
) {
  const body = { ...request, receipt: receiptProof(receipt, signal) }
  const bodyJSON = JSON.stringify(body)
  if (Buffer.byteLength(bodyJSON) > 48 * 1024 * 1024)
    throw new ReceiptClientError('invalid_request')
  return {
    client,
    body,
    bodySerializer: () => bodyJSON,
    path: receiptPath(receipt),
    signal,
    redirect: 'error' as const,
    fetch: receiptFetch(fetch, maxReceiptResponseBytes, signal),
    responseValidator: inputResponseValidator,
  }
}

function receiptPath(receipt: Readonly<ChannelConnectorEventReceipt>) {
  return {
    integrationAppID: receipt.integration_app_id,
    integrationInstallID: receipt.integration_install_id,
  }
}

function receiptProof(receipt: Readonly<ChannelConnectorEventReceipt>, signal: AbortSignal) {
  signal.throwIfAborted()
  if (!Number.isSafeInteger(receipt.lease_generation))
    throw new ReceiptClientError('invalid_request')
  return {
    receipt_id: receipt.receipt_id,
    lease_token: receipt.lease_token,
    lease_generation: receipt.lease_generation,
  }
}

const inputResponseValidator: NonNullable<
  Parameters<typeof sdk.deliverChannelConnectorInput>[0]['responseValidator']
> = (data) =>
  schemas.zChannelConnectorInputResponse.safeParse(data).success
    ? Promise.resolve()
    : Promise.reject(new ReceiptClientError('invalid_response'))

/** Discover the selected installation's inbound behavior before admission. */
export async function listInputRoutes(
  client: Client,
  fetch: typeof globalThis.fetch,
  receipt: Readonly<ChannelConnectorEventReceipt>,
  signal: AbortSignal,
  work?: ProviderWorkReservation,
): Promise<ListChannelConnectorRoutesResponse['routes']> {
  try {
    const { data, response } = await sdk.listChannelConnectorRoutes({
      client,
      path: {
        integrationAppID: receipt.integration_app_id,
        integrationInstallID: receipt.integration_install_id,
      },
      signal,
      redirect: 'error',
      fetch: receiptFetch(fetch, 17 * 1024 * 1024, signal, work),
    })
    if (
      response.status !== 200 ||
      !schemas.zListChannelConnectorRoutesResponse.safeParse(data).success
    )
      throw new ReceiptClientError('invalid_response')
    return data.routes
  } catch (cause) {
    throw receiptFailure(cause, signal)
  }
}

/** Publish the provider contract needed before admitting a conversation's input. */
export async function publishInputDefinition(
  client: Client,
  fetch: typeof globalThis.fetch,
  scope: Readonly<
    Pick<ChannelConnectorEventReceipt, 'integration_app_id' | 'integration_install_id'>
  >,
  body: PublishChannelConnectorDefinitionRequest,
  signal: AbortSignal,
): Promise<ChannelDefinition> {
  try {
    if (!schemas.zPublishChannelConnectorDefinitionRequest.safeParse(body).success)
      throw new ReceiptClientError('invalid_request')
    const { data, response } = await sdk.publishChannelConnectorDefinition({
      body,
      client: client,
      path: {
        integrationAppID: scope.integration_app_id,
        integrationInstallID: scope.integration_install_id,
      },
      signal,
      redirect: 'error',
      fetch: receiptFetch(fetch, 512 * 1024, signal),
    })
    if (
      response.status !== 200 ||
      !schemas.zChannelDefinition.safeParse(data).success ||
      data.implementation_key !== body.implementation_key ||
      data.kind !== body.kind
    )
      throw new ReceiptClientError('invalid_response')
    return data
  } catch (cause) {
    throw receiptFailure(cause, signal)
  }
}

export async function submitInboundEvent(
  client: Client,
  fetch: typeof globalThis.fetch,
  integrationAppId: string,
  event: ChannelInboundEventRequest,
  signal: AbortSignal,
  random: () => number,
): Promise<ChannelInboundEventResponse> {
  try {
    const bodyJSON = JSON.stringify(event)
    return await retryCoreRequest(
      signal,
      async () => {
        const { data, response } = await sdk.acceptChannelConnectorEvent({
          body: event,
          bodySerializer: () => bodyJSON,
          client,
          fetch: receiptFetch(fetch, 64 * 1024, signal),
          redirect: 'error',
          path: { integrationAppID: integrationAppId },
          signal,
        })
        if (
          response.status !== 202 ||
          !schemas.zChannelInboundEventResponse.safeParse(data).success
        )
          throw new ReceiptClientError('invalid_response')
        return requireData(data)
      },
      random,
    )
  } catch (cause) {
    throw receiptFailure(cause, signal)
  }
}
