import {
  bearerToken,
  type ChannelConnectorAppConfiguration,
  type ChannelConnectorCapability,
  type ChannelConnectorControlReceipt,
  type ChannelConnectorEventReceipt,
  type ChannelConnectorInstallationConfiguration,
  type ChannelConnectorRuntimeUnit,
  type ChannelDefinition,
  type ChannelInboundControlEventRequest,
  type ChannelInboundEventRequest,
  type ChannelInboundEventResponse,
  createOmnaraClient,
  type HeartbeatChannelConnectorRuntimeUnitRequest,
  type LookupChannelConnectorGitHubReviewsRequest,
  type LookupChannelConnectorGitHubReviewsResponse,
  type LookupChannelConnectorWorkflowRequest,
  type PublishChannelConnectorDefinitionRequest,
  type RecordChannelConnectorGitHubReviewRequest,
  type RecordChannelConnectorGitHubReviewResponse,
  type ReleaseChannelConnectorRuntimeUnitRequest,
  type ResolveChannelConnectorInteractionRequest,
  type ResolveChannelConnectorInteractionResponse,
  schemas,
  sdk,
  type SetChannelConnectorInstallationProviderStateRequest,
} from '@omnara/sdk'

import {
  claimNextControlEvent,
  completeControlEvent,
  type ControlCompletion,
  submitControlEvent,
} from './core-controls'
import {
  type GitHubReviewInstallation,
  lookupGitHubReviews,
  recordGitHubReview,
} from './core-github-reviews'
import { receiptFailure, requireData, retryCoreRequest } from './core-http'
import {
  deliverBoundInput,
  deliverWorkflowInput,
  listInputRoutes,
  lookupInputRecipients,
  lookupWorkflowInput,
  publishInputDefinition,
  type ReceiptInputRequest,
  type ReceiptRecipientsRequest,
  type ReceiptWorkflowRequest,
  submitInboundEvent,
} from './core-inputs'
import {
  type InstallationControlQuery,
  listInstallationControlScopes,
  setInstallationProviderState,
} from './core-provider-state'
import { claimNextEvent, completeEvent, type ReceiptCompletion } from './core-receipts'
import { ReceiptClientError, receiptFetch } from './receipt-http'
import type { ProviderWorkReservation, RuntimeCheckpoint } from './types'

export type {
  ReceiptInputRequest,
  ReceiptRecipientsRequest,
  ReceiptWorkflowRequest,
} from './core-inputs'
export type { ReceiptCompletion } from './core-receipts'

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

  listInstallationControlScopes(
    appId: string,
    query: InstallationControlQuery,
    signal: AbortSignal,
    work?: ProviderWorkReservation,
  ) {
    return listInstallationControlScopes(
      this.client,
      appId,
      query,
      this.requestSignal(signal),
      work,
    )
  }

  setInstallationProviderState(
    appId: string,
    installId: string,
    body: SetChannelConnectorInstallationProviderStateRequest,
    signal: AbortSignal,
  ) {
    return setInstallationProviderState(
      this.client,
      appId,
      installId,
      body,
      this.requestSignal(signal),
    )
  }

  async submitInbound(
    integrationAppId: string,
    event: ChannelInboundEventRequest,
    signal?: AbortSignal,
  ): Promise<ChannelInboundEventResponse> {
    return submitInboundEvent(
      this.client,
      this.fetch,
      integrationAppId,
      event,
      this.requestSignal(signal),
      this.random,
    )
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

  claimNextEvent(
    capability: ChannelConnectorCapability,
    leaseMs: number,
    signal?: AbortSignal,
    work?: ProviderWorkReservation,
  ) {
    return claimNextEvent(
      this.client,
      this.fetch,
      capability,
      leaseMs,
      this.requestSignal(signal),
      work,
    )
  }

  completeEvent(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    completion: ReceiptCompletion,
    signal?: AbortSignal,
  ) {
    return completeEvent(this.client, this.fetch, receipt, completion, this.requestSignal(signal))
  }

  async listRoutes(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    parentSignal: AbortSignal,
    work?: ProviderWorkReservation,
  ) {
    return listInputRoutes(this.client, this.fetch, receipt, this.requestSignal(parentSignal), work)
  }

  async submitControlEvent(
    appId: string,
    event: ChannelInboundControlEventRequest,
    signal: AbortSignal,
  ) {
    return submitControlEvent(this.client, this.fetch, appId, event, this.requestSignal(signal))
  }

  async claimNextControlEvent(
    capability: ChannelConnectorCapability,
    leaseMs: number,
    signal: AbortSignal,
    work?: ProviderWorkReservation,
  ) {
    return claimNextControlEvent(
      this.client,
      this.fetch,
      capability,
      leaseMs,
      this.requestSignal(signal),
      work,
    )
  }

  async completeControlEvent(
    receipt: Readonly<ChannelConnectorControlReceipt>,
    completion: ControlCompletion,
    signal: AbortSignal,
  ) {
    return completeControlEvent(
      this.client,
      this.fetch,
      receipt,
      completion,
      this.requestSignal(signal),
    )
  }

  async publishDefinition(
    scope: Readonly<
      Pick<ChannelConnectorEventReceipt, 'integration_app_id' | 'integration_install_id'>
    >,
    body: PublishChannelConnectorDefinitionRequest,
    parentSignal: AbortSignal,
  ): Promise<ChannelDefinition> {
    return publishInputDefinition(
      this.client,
      this.fetch,
      scope,
      body,
      this.requestSignal(parentSignal),
    )
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

  async lookupGitHubReviews(
    installation: GitHubReviewInstallation,
    request: LookupChannelConnectorGitHubReviewsRequest,
    signal?: AbortSignal,
  ): Promise<LookupChannelConnectorGitHubReviewsResponse> {
    const requestSignal = this.requestSignal(signal)
    return this.retryCoreRequest(requestSignal, () =>
      lookupGitHubReviews(this.client, installation, request, requestSignal),
    )
  }

  async recordGitHubReview(
    installation: GitHubReviewInstallation,
    request: RecordChannelConnectorGitHubReviewRequest,
    signal?: AbortSignal,
  ): Promise<RecordChannelConnectorGitHubReviewResponse> {
    const requestSignal = this.requestSignal(signal)
    // Identical acknowledgment is idempotent and grants no replacement authority.
    return this.retryCoreRequest(requestSignal, () =>
      recordGitHubReview(this.client, installation, request, requestSignal),
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
