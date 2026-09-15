import {
  bearerToken,
  type ChannelConnectorAppConfiguration,
  type ChannelConnectorCapability,
  type ChannelConnectorEventReceipt,
  type ChannelConnectorInstallationConfiguration,
  type ChannelConnectorRuntimeUnit,
  type ChannelDefinition,
  type ChannelInboundEventRequest,
  type ChannelInboundEventResponse,
  type CompleteChannelConnectorEventRequest,
  createOmnaraClient,
  type HeartbeatChannelConnectorRuntimeUnitRequest,
  type ListChannelConnectorRoutesResponse,
  type LookupChannelConnectorWorkflowRequest,
  type PublishChannelConnectorDefinitionRequest,
  type ReleaseChannelConnectorRuntimeUnitRequest,
  type ResolveChannelConnectorInteractionRequest,
  type ResolveChannelConnectorInteractionResponse,
  schemas,
  sdk,
} from '@omnara/sdk'

import { receiptFailure, requireData, retryCoreRequest } from './core-http'
import {
  deliverBoundInput,
  deliverWorkflowInput,
  lookupInputRecipients,
  lookupWorkflowInput,
  type ReceiptInputRequest,
  type ReceiptRecipientsRequest,
  type ReceiptWorkflowRequest,
} from './core-inputs'
import { maxReceiptResponseBytes, ReceiptClientError, receiptFetch } from './receipt-http'
import type { ProviderWorkReservation, RuntimeCheckpoint } from './types'

export type {
  ReceiptInputRequest,
  ReceiptRecipientsRequest,
  ReceiptWorkflowRequest,
} from './core-inputs'
export type ReceiptCompletion = Pick<CompleteChannelConnectorEventRequest, 'state' | 'last_error'>

export interface CoreClientOptions {
  baseUrl: string
  fetch?: typeof fetch
  random?: () => number
  requestTimeoutMs?: number
  token: string
}

export class CoreClient {
  private readonly client
  private readonly random: () => number
  private readonly requestTimeoutMs: number
  private readonly fetch: typeof globalThis.fetch

  constructor(options: CoreClientOptions) {
    this.requestTimeoutMs = options.requestTimeoutMs ?? 10_000
    this.random = options.random ?? Math.random
    this.fetch = options.fetch ?? globalThis.fetch
    this.client = createOmnaraClient({
      auth: bearerToken(options.token),
      baseUrl: options.baseUrl,
    })
    if (options.fetch) this.client.setConfig({ fetch: options.fetch })
  }

  async getAppConfiguration(
    integrationAppId: string,
    parentSignal?: AbortSignal,
  ): Promise<ChannelConnectorAppConfiguration> {
    const signal = this.requestSignal(parentSignal)
    return this.retryCoreRequest(signal, async () => {
      const { data } = await sdk.getChannelConnectorAppConfiguration({
        client: this.client,
        path: { integrationAppID: integrationAppId },
        signal,
      })
      return requireData(data)
    })
  }

  async getInstallationConfiguration(
    integrationAppId: string,
    integrationInstallId: string,
    signal?: AbortSignal,
  ): Promise<ChannelConnectorInstallationConfiguration> {
    const requestSignal = this.requestSignal(signal)
    return this.retryCoreRequest(requestSignal, async () => {
      const { data } = await sdk.getChannelConnectorInstallationConfiguration({
        client: this.client,
        path: {
          integrationAppID: integrationAppId,
          integrationInstallID: integrationInstallId,
        },
        signal: requestSignal,
      })
      return requireData(data)
    })
  }

  async resolveInstallationConfiguration(
    integrationAppId: string,
    externalTenantId: string,
    externalAccountRef: string,
    signal?: AbortSignal,
  ): Promise<ChannelConnectorInstallationConfiguration> {
    const requestSignal = this.requestSignal(signal)
    return this.retryCoreRequest(requestSignal, async () => {
      const { data } = await sdk.resolveChannelConnectorInstallationConfiguration({
        client: this.client,
        path: { integrationAppID: integrationAppId },
        query: {
          external_account_ref: externalAccountRef,
          external_tenant_id: externalTenantId,
        },
        signal: requestSignal,
      })
      return requireData(data)
    })
  }

  async submitInbound(
    integrationAppId: string,
    event: ChannelInboundEventRequest,
    signal?: AbortSignal,
  ): Promise<ChannelInboundEventResponse> {
    const requestSignal = this.requestSignal(signal)
    try {
      const bodyJSON = JSON.stringify(event)
      return await this.retryCoreRequest(requestSignal, async () => {
        const { data, response } = await sdk.acceptChannelConnectorEvent({
          body: event,
          bodySerializer: () => bodyJSON,
          client: this.client,
          fetch: receiptFetch(this.fetch, 64 * 1024, requestSignal),
          redirect: 'error',
          path: { integrationAppID: integrationAppId },
          signal: requestSignal,
        })
        if (
          response.status !== 202 ||
          !schemas.zChannelInboundEventResponse.safeParse(data).success
        )
          throw new ReceiptClientError('invalid_response')
        return requireData(data)
      })
    } catch (cause) {
      throw receiptFailure(cause, requestSignal)
    }
  }

  async submitRuntimeInbound(
    integrationAppId: string,
    unit: ChannelConnectorRuntimeUnit,
    event: ChannelInboundEventRequest,
    signal?: AbortSignal,
  ): Promise<ChannelInboundEventResponse> {
    if (!unit.lease_token) throw new Error('claimed runtime unit is missing its lease token')
    const leaseToken = unit.lease_token
    const requestSignal = this.requestSignal(signal)
    const runtimeUnitId = unit.id
    try {
      const bodyJSON = JSON.stringify({
        event,
        lease_generation: unit.lease_generation,
        lease_token: leaseToken,
      })
      return await this.retryCoreRequest(requestSignal, async () => {
        const { data, response } = await sdk.acceptChannelConnectorRuntimeEvent({
          bodySerializer: () => bodyJSON,
          body: {
            event,
            lease_generation: unit.lease_generation,
            lease_token: leaseToken,
          },
          client: this.client,
          fetch: receiptFetch(this.fetch, 64 * 1024, requestSignal),
          redirect: 'error',
          path: { integrationAppID: integrationAppId, runtimeUnitID: runtimeUnitId },
          signal: requestSignal,
        })
        if (
          response.status !== 202 ||
          !schemas.zChannelInboundEventResponse.safeParse(data).success
        )
          throw new ReceiptClientError('invalid_response')
        return requireData(data)
      })
    } catch (cause) {
      throw receiptFailure(cause, requestSignal)
    }
  }

  /** Exactly one claim POST; a lost response is not permission to claim again. */
  async claimNextEvent(
    capability: ChannelConnectorCapability,
    leaseMs: number,
    parentSignal?: AbortSignal,
    work?: ProviderWorkReservation,
  ): Promise<ChannelConnectorEventReceipt | undefined> {
    const signal = this.requestSignal(parentSignal)
    try {
      const { data, response } = await sdk.claimNextChannelConnectorEvent({
        body: { capability, lease_ms: leaseMs },
        client: this.client,
        fetch: receiptFetch(this.fetch, maxReceiptResponseBytes, signal, work),
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
  async completeEvent(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    completion: ReceiptCompletion,
    parentSignal?: AbortSignal,
  ): Promise<void> {
    const signal = this.requestSignal(parentSignal)
    try {
      const { data, response } = await sdk.completeChannelConnectorEvent({
        body: {
          state: completion.state,
          last_error: completion.last_error,
          lease_token: receipt.lease_token,
          lease_generation: receipt.lease_generation,
        },
        client: this.client,
        fetch: receiptFetch(this.fetch, 64 * 1024, signal),
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

  async listRoutes(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    parentSignal: AbortSignal,
    work?: ProviderWorkReservation,
  ): Promise<ListChannelConnectorRoutesResponse['routes']> {
    const signal = this.requestSignal(parentSignal)
    try {
      const { data, response } = await sdk.listChannelConnectorRoutes({
        client: this.client,
        path: {
          integrationAppID: receipt.integration_app_id,
          integrationInstallID: receipt.integration_install_id,
        },
        signal,
        redirect: 'error',
        fetch: receiptFetch(this.fetch, 17 * 1024 * 1024, signal, work),
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

  async publishDefinition(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    body: PublishChannelConnectorDefinitionRequest,
    parentSignal: AbortSignal,
  ): Promise<ChannelDefinition> {
    const signal = this.requestSignal(parentSignal)
    try {
      if (!schemas.zPublishChannelConnectorDefinitionRequest.safeParse(body).success)
        throw new ReceiptClientError('invalid_request')
      const { data, response } = await sdk.publishChannelConnectorDefinition({
        body,
        client: this.client,
        path: {
          integrationAppID: receipt.integration_app_id,
          integrationInstallID: receipt.integration_install_id,
        },
        signal,
        redirect: 'error',
        fetch: receiptFetch(this.fetch, 512 * 1024, signal),
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

  async lookupWorkflow(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    body: LookupChannelConnectorWorkflowRequest,
    parentSignal: AbortSignal,
  ) {
    return lookupWorkflowInput(
      this.client,
      this.fetch,
      receipt,
      body,
      this.requestSignal(parentSignal),
    )
  }

  /** Receipt-scoped discovery precedes both bound input and workflow admission. */
  async lookupRecipients(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    request: ReceiptRecipientsRequest,
    parentSignal: AbortSignal,
  ) {
    return lookupInputRecipients(
      this.client,
      this.fetch,
      receipt,
      request,
      this.requestSignal(parentSignal),
    )
  }

  async deliverInput(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    request: ReceiptInputRequest,
    parentSignal: AbortSignal,
  ) {
    return deliverBoundInput(
      this.client,
      this.fetch,
      receipt,
      request,
      this.requestSignal(parentSignal),
    )
  }

  async deliverWorkflow(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    request: ReceiptWorkflowRequest,
    parentSignal: AbortSignal,
  ) {
    return deliverWorkflowInput(
      this.client,
      this.fetch,
      receipt,
      request,
      this.requestSignal(parentSignal),
    )
  }

  async resolveInteraction(
    integrationAppId: string,
    interactionId: string,
    request: ResolveChannelConnectorInteractionRequest,
    signal?: AbortSignal,
  ): Promise<ResolveChannelConnectorInteractionResponse> {
    const requestSignal = this.requestSignal(signal)
    return this.retryCoreRequest(requestSignal, async () => {
      const { data } = await sdk.resolveChannelConnectorInteraction({
        body: request,
        client: this.client,
        path: { integrationAppID: integrationAppId, interactionID: interactionId },
        signal: requestSignal,
      })
      return requireData(data)
    })
  }

  async resolveRuntimeInteraction(
    integrationAppId: string,
    unit: ChannelConnectorRuntimeUnit,
    interactionId: string,
    request: ResolveChannelConnectorInteractionRequest,
    signal?: AbortSignal,
  ): Promise<ResolveChannelConnectorInteractionResponse> {
    if (!unit.lease_token) throw new Error('claimed runtime unit is missing its lease token')
    const leaseToken = unit.lease_token
    const requestSignal = this.requestSignal(signal)
    return this.retryCoreRequest(requestSignal, async () => {
      const { data } = await sdk.resolveChannelConnectorRuntimeInteraction({
        body: {
          interaction: request,
          lease_generation: unit.lease_generation,
          lease_token: leaseToken,
        },
        client: this.client,
        path: {
          integrationAppID: integrationAppId,
          interactionID: interactionId,
          runtimeUnitID: unit.id,
        },
        signal: requestSignal,
      })
      return requireData(data)
    })
  }

  async claimRuntimeUnits(
    capability: ChannelConnectorCapability,
    owner: string,
    leaseMs: number,
    limit: number,
    signal?: AbortSignal,
  ): Promise<ChannelConnectorRuntimeUnit[]> {
    const { data } = await sdk.claimChannelConnectorRuntimeUnits({
      body: { capability, lease_ms: leaseMs, limit, owner },
      client: this.client,
      signal: this.requestSignal(signal),
    })
    return requireData(data).runtime_units
  }

  async heartbeatRuntimeUnit(
    unit: ChannelConnectorRuntimeUnit,
    leaseMs: number,
    checkpoint?: RuntimeCheckpoint,
    signal?: AbortSignal,
  ): Promise<ChannelConnectorRuntimeUnit> {
    if (!unit.lease_token) throw new Error('claimed runtime unit is missing its lease token')
    const leaseToken = unit.lease_token
    const requestSignal = this.requestSignal(signal)
    return this.retryCoreRequest(requestSignal, async () => {
      const body: HeartbeatChannelConnectorRuntimeUnitRequest = {
        lease_generation: unit.lease_generation,
        lease_ms: leaseMs,
        lease_token: leaseToken,
      }
      if (checkpoint) {
        body.checkpoint = checkpoint.checkpoint
        body.checkpoint_version = checkpoint.version
      }
      const { data } = await sdk.heartbeatChannelConnectorRuntimeUnit({
        body,
        client: this.client,
        path: { runtimeUnitID: unit.id },
        signal: requestSignal,
      })
      return requireData(data)
    })
  }

  async releaseRuntimeUnit(
    unit: ChannelConnectorRuntimeUnit,
    lastError: ReleaseChannelConnectorRuntimeUnitRequest['last_error'],
    checkpoint?: RuntimeCheckpoint,
    parentSignal?: AbortSignal,
  ): Promise<void> {
    if (!unit.lease_token) return
    const leaseToken = unit.lease_token
    const signal = this.requestSignal(parentSignal)
    await this.retryCoreRequest(signal, async () => {
      const body: ReleaseChannelConnectorRuntimeUnitRequest = {
        last_error: lastError,
        lease_generation: unit.lease_generation,
        lease_token: leaseToken,
      }
      if (checkpoint) {
        body.checkpoint = checkpoint.checkpoint
        body.checkpoint_version = checkpoint.version
      }
      await sdk.releaseChannelConnectorRuntimeUnit({
        body,
        client: this.client,
        path: { runtimeUnitID: unit.id },
        signal,
      })
    })
  }

  private retryCoreRequest<T>(signal: AbortSignal, request: () => Promise<T>): Promise<T> {
    return retryCoreRequest(signal, request, this.random)
  }

  private requestSignal(parent?: AbortSignal): AbortSignal {
    const timeout = AbortSignal.timeout(this.requestTimeoutMs)
    return parent ? AbortSignal.any([parent, timeout]) : timeout
  }
}
