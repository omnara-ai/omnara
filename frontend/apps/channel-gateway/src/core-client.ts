import {
  ApiError,
  bearerToken,
  type ChannelConnectorAppConfiguration,
  type ChannelConnectorCapability,
  type ChannelConnectorDelivery,
  type ChannelConnectorInstallationConfiguration,
  type ChannelConnectorRuntimeUnit,
  type ChannelInboundEventRequest,
  type CompleteChannelConnectorDeliveryRequest,
  createOmnaraClient,
  type ResolveChannelConnectorInteractionRequest,
  type ResolveChannelConnectorInteractionResponse,
  sdk,
} from '@omnara/sdk'

import { abortableDelay, equalJitterMilliseconds } from './async'
import type { RuntimeCheckpoint } from './types'

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

  constructor(options: CoreClientOptions) {
    this.requestTimeoutMs = options.requestTimeoutMs ?? 10_000
    this.random = options.random ?? Math.random
    this.client = createOmnaraClient({
      auth: bearerToken(options.token),
      baseUrl: options.baseUrl,
    })
    if (options.fetch) this.client.setConfig({ fetch: options.fetch })
  }

  async getAppConfiguration(integrationAppId: string): Promise<ChannelConnectorAppConfiguration> {
    const signal = this.requestSignal()
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
  ): Promise<void> {
    const requestSignal = this.requestSignal(signal)
    await this.retryCoreRequest(requestSignal, async () => {
      await sdk.acceptChannelConnectorEvent({
        body: event,
        client: this.client,
        path: { integrationAppID: integrationAppId },
        signal: requestSignal,
      })
    })
  }

  async submitRuntimeInbound(
    integrationAppId: string,
    unit: ChannelConnectorRuntimeUnit,
    event: ChannelInboundEventRequest,
    signal?: AbortSignal,
  ): Promise<void> {
    if (!unit.lease_token) throw new Error('claimed runtime unit is missing its lease token')
    const leaseToken = unit.lease_token
    const requestSignal = this.requestSignal(signal)
    await this.retryCoreRequest(requestSignal, async () => {
      await sdk.acceptChannelConnectorRuntimeEvent({
        body: {
          event,
          lease_generation: unit.lease_generation,
          lease_token: leaseToken,
        },
        client: this.client,
        path: { integrationAppID: integrationAppId, runtimeUnitID: unit.id },
        signal: requestSignal,
      })
    })
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

  async claimDeliveries(
    capability: ChannelConnectorCapability,
    owner: string,
    leaseMs: number,
    limit: number,
    signal?: AbortSignal,
  ): Promise<ChannelConnectorDelivery[]> {
    const { data } = await sdk.claimChannelConnectorDeliveries({
      body: { capability, lease_ms: leaseMs, limit, owner },
      client: this.client,
      signal: this.requestSignal(signal),
    })
    return requireData(data).deliveries
  }

  async completeDelivery(
    delivery: ChannelConnectorDelivery,
    completion: CompleteChannelConnectorDeliveryRequest,
    parentSignal?: AbortSignal,
  ): Promise<void> {
    const signal = this.requestSignal(parentSignal)
    await this.retryCoreRequest(signal, async () => {
      await sdk.completeChannelConnectorDelivery({
        body: completion,
        client: this.client,
        path: { deliveryID: delivery.id },
        signal,
      })
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
      const { data } = await sdk.heartbeatChannelConnectorRuntimeUnit({
        body: {
          lease_generation: unit.lease_generation,
          lease_ms: leaseMs,
          lease_token: leaseToken,
          ...(checkpoint
            ? { checkpoint: checkpoint.checkpoint, checkpoint_version: checkpoint.version }
            : {}),
        },
        client: this.client,
        path: { runtimeUnitID: unit.id },
        signal: requestSignal,
      })
      return requireData(data)
    })
  }

  async releaseRuntimeUnit(
    unit: ChannelConnectorRuntimeUnit,
    lastError: Record<string, unknown>,
    checkpoint?: RuntimeCheckpoint,
    parentSignal?: AbortSignal,
  ): Promise<void> {
    if (!unit.lease_token) return
    const leaseToken = unit.lease_token
    const signal = this.requestSignal(parentSignal)
    await this.retryCoreRequest(signal, async () => {
      await sdk.releaseChannelConnectorRuntimeUnit({
        body: {
          last_error: lastError,
          lease_generation: unit.lease_generation,
          lease_token: leaseToken,
          ...(checkpoint
            ? { checkpoint: checkpoint.checkpoint, checkpoint_version: checkpoint.version }
            : {}),
        },
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

async function retryCoreRequest<T>(
  signal: AbortSignal,
  request: () => Promise<T>,
  random: () => number,
): Promise<T> {
  const delays = [0, 100, 250, 500, 1_000]
  let lastError: unknown
  for (const delay of delays) {
    if (delay > 0 && !(await abortableDelay(equalJitterMilliseconds(delay, random), signal))) {
      throw abortReason(signal)
    }
    try {
      return await request()
    } catch (error) {
      lastError = error
      if (!isTransientCoreError(error)) throw error
    }
  }
  throw lastError
}

function requireData<T>(value: T | undefined): T {
  if (value === undefined) throw new Error('Omnara API returned no response data')
  return value
}

export function isTransientCoreError(error: unknown): boolean {
  if (error instanceof ApiError) {
    return error.status === 429 || error.status >= 500
  }
  return (
    error instanceof TypeError || (error instanceof DOMException && error.name === 'TimeoutError')
  )
}

export function isCoreNotFoundError(error: unknown): error is ApiError {
  return error instanceof ApiError && error.status === 404
}

function abortReason(signal: AbortSignal): unknown {
  return signal.reason ?? new Error('Omnara API request was aborted')
}
