import type {
  ChannelConnectorCapability,
  ChannelConnectorDelivery,
  CompleteChannelConnectorDeliveryRequest,
} from '@omnara/sdk'

import type { AppRuntimeRegistry } from './app-registry'
import { abortableDelay, pollJitterMilliseconds } from './async'
import type { CoreClient } from './core-client'
import { deliverySafetyMarginMs } from './delivery-timing'
import { errorMessage } from './diagnostics'
import { type GatewayLogger, ProviderDeliveryError } from './types'

const maxDeliveryAttempts = 8
const maxProviderMessageRefBytes = 2048

export interface DeliveryLoopOptions {
  capabilities: ChannelConnectorCapability[]
  claimLimit: number
  client: CoreClient
  completionTimeoutMs: number
  idlePollMs: number
  leaseMs: number
  logger: GatewayLogger
  owner: string
  random?: () => number
  registry: AppRuntimeRegistry
  sendTimeoutMs: number
}

export class DeliveryLoop {
  constructor(private readonly options: DeliveryLoopOptions) {}

  async run(signal: AbortSignal): Promise<void> {
    if (this.options.capabilities.length === 0) return
    let cursor = 0
    let emptyClaims = 0
    while (!signal.aborted) {
      const capability = this.options.capabilities[cursor]
      if (!capability) return
      cursor = (cursor + 1) % this.options.capabilities.length
      try {
        const claimStartedAtMs = Date.now()
        const deliveries = await this.options.client.claimDeliveries(
          capability,
          this.options.owner,
          this.options.leaseMs,
          this.options.claimLimit,
          signal,
        )
        if (deliveries.length === 0) {
          emptyClaims++
          if (emptyClaims >= this.options.capabilities.length) {
            emptyClaims = 0
            await this.pollDelay(signal)
          }
          continue
        }
        emptyClaims = 0
        const localLeaseDeadlineMs = claimStartedAtMs + this.options.leaseMs
        await Promise.all(
          deliveries.map((delivery) => this.process(delivery, localLeaseDeadlineMs, signal)),
        )
      } catch (error) {
        if (isAborted(signal)) return
        this.options.logger.error('channel delivery claim loop failed', {
          connector_key: capability.connector_key,
          error: errorMessage(error),
          provider: capability.provider,
        })
        emptyClaims = 0
        await this.pollDelay(signal)
      }
    }
  }

  private pollDelay(signal: AbortSignal): Promise<boolean> {
    return abortableDelay(
      pollJitterMilliseconds(this.options.idlePollMs, this.options.random),
      signal,
    )
  }

  private async process(
    delivery: ChannelConnectorDelivery,
    localLeaseDeadlineMs: number,
    shutdownSignal: AbortSignal,
  ): Promise<void> {
    if (shutdownSignal.aborted) {
      await this.complete(
        delivery,
        unattemptedRetry(delivery, abortReason(shutdownSignal), this.options.random),
        localLeaseDeadlineMs,
      )
      return
    }
    const sendTimeoutMs = remainingSendTimeMs(
      this.options.sendTimeoutMs,
      this.options.completionTimeoutMs,
      localLeaseDeadlineMs,
      this.options.leaseMs,
    )
    if (sendTimeoutMs <= 0) {
      await this.complete(
        delivery,
        unattemptedRetry(
          delivery,
          new ProviderDeliveryError('delivery lease has too little time remaining to send safely', {
            retryAfterMs: 100,
            retryable: true,
          }),
          this.options.random,
        ),
        localLeaseDeadlineMs,
      )
      return
    }
    const sendSignal = AbortSignal.timeout(sendTimeoutMs)
    const preparationSignal = AbortSignal.any([shutdownSignal, sendSignal])
    const sendDeadlineMs = Date.now() + sendTimeoutMs
    let handle
    try {
      handle = await acquireRuntimeHandle(
        this.options.registry,
        delivery.integration_app_id,
        requiredConfigurationRevision(delivery.app_configuration_revision, 'app'),
        preparationSignal,
      )
    } catch (error) {
      await this.complete(
        delivery,
        preparationExpired(preparationSignal, sendDeadlineMs, localLeaseDeadlineMs, this.options)
          ? unattemptedRetry(delivery, error, this.options.random)
          : preSendFailure(delivery, error, this.options.random),
        localLeaseDeadlineMs,
      )
      return
    }
    try {
      let installation
      try {
        installation = await raceWithAbort(
          handle.getInstallation(
            delivery.integration_install_id,
            requiredConfigurationRevision(delivery.install_configuration_revision, 'installation'),
          ),
          preparationSignal,
        )
        if (
          preparationExpired(preparationSignal, sendDeadlineMs, localLeaseDeadlineMs, this.options)
        ) {
          throw new ProviderDeliveryError(
            'delivery deadline elapsed while preparing the installation',
            {
              retryAfterMs: 100,
              retryable: true,
            },
          )
        }
      } catch (error) {
        await this.complete(
          delivery,
          preparationExpired(preparationSignal, sendDeadlineMs, localLeaseDeadlineMs, this.options)
            ? unattemptedRetry(delivery, error, this.options.random)
            : preSendFailure(delivery, error, this.options.random),
          localLeaseDeadlineMs,
        )
        return
      }
      try {
        const result = await raceWithAbort(
          handle.runtime.send(delivery, { installation, signal: sendSignal }),
          sendSignal,
        )
        await this.complete(
          delivery,
          {
            claim_generation: delivery.claim_generation,
            claim_token: requiredClaimToken(delivery),
            last_error: {},
            outcome: 'delivered',
            provider_message_ref: safeProviderMessageRef(
              result.providerMessageRef,
              delivery,
              this.options.logger,
            ),
          },
          localLeaseDeadlineMs,
        )
      } catch (error) {
        await this.complete(
          delivery,
          sendFailure(delivery, error, this.options.random),
          localLeaseDeadlineMs,
        )
      }
    } finally {
      await handle.release()
    }
  }

  private async complete(
    delivery: ChannelConnectorDelivery,
    completion: CompleteChannelConnectorDeliveryRequest,
    localLeaseDeadlineMs: number,
  ): Promise<void> {
    try {
      await this.options.client.completeDelivery(
        delivery,
        completion,
        AbortSignal.timeout(remainingCompletionTimeMs(localLeaseDeadlineMs, this.options)),
      )
    } catch (error) {
      this.options.logger.error('complete channel delivery', {
        delivery_id: delivery.id,
        error: errorMessage(error),
        outcome: completion.outcome,
      })
    }
  }
}

function unattemptedRetry(
  delivery: ChannelConnectorDelivery,
  error: unknown,
  random?: () => number,
): CompleteChannelConnectorDeliveryRequest {
  return retryCompletion(delivery, error, retryDelay(delivery, error, random))
}

function preSendFailure(
  delivery: ChannelConnectorDelivery,
  error: unknown,
  random?: () => number,
): CompleteChannelConnectorDeliveryRequest {
  const terminal = error instanceof ProviderDeliveryError && !error.retryable
  if (!terminal && delivery.attempt_count < maxDeliveryAttempts) {
    return retryCompletion(delivery, error, retryDelay(delivery, error, random))
  }
  return failureCompletion(delivery, error, 'failed')
}

function sendFailure(
  delivery: ChannelConnectorDelivery,
  error: unknown,
  random?: () => number,
): CompleteChannelConnectorDeliveryRequest {
  if (error instanceof ProviderDeliveryError) {
    if (error.outcomeUnknown) return failureCompletion(delivery, error, 'unknown')
    if (error.retryable && delivery.attempt_count < maxDeliveryAttempts) {
      return retryCompletion(delivery, error, retryDelay(delivery, error, random))
    }
    return failureCompletion(delivery, error, 'failed')
  }
  return failureCompletion(delivery, error, 'unknown')
}

function retryCompletion(
  delivery: ChannelConnectorDelivery,
  error: unknown,
  retryAfterMs: number,
): CompleteChannelConnectorDeliveryRequest {
  return {
    claim_generation: delivery.claim_generation,
    claim_token: requiredClaimToken(delivery),
    last_error: { code: 'transient_failure', message: errorMessage(error) },
    outcome: 'retry_wait',
    provider_message_ref: '',
    retry_after_ms: retryAfterMs,
  }
}

function failureCompletion(
  delivery: ChannelConnectorDelivery,
  error: unknown,
  outcome: 'failed' | 'unknown',
): CompleteChannelConnectorDeliveryRequest {
  return {
    claim_generation: delivery.claim_generation,
    claim_token: requiredClaimToken(delivery),
    last_error: {
      code: outcome === 'unknown' ? 'outcome_unknown' : 'permanent_failure',
      message: errorMessage(error),
    },
    outcome,
    provider_message_ref: '',
  }
}

function requiredClaimToken(delivery: ChannelConnectorDelivery): string {
  if (!delivery.claim_token) throw new Error('claimed delivery is missing its claim token')
  return delivery.claim_token
}

function safeProviderMessageRef(
  value: unknown,
  delivery: ChannelConnectorDelivery,
  logger: GatewayLogger,
): string {
  if (typeof value !== 'string') {
    logger.warn('channel provider message reference omitted', {
      delivery_id: delivery.id,
      provider_message_ref_type: typeof value,
    })
    return ''
  }
  const normalized = value.trim()
  const byteLength = new TextEncoder().encode(normalized).byteLength
  if (!normalized.includes('\u0000') && byteLength <= maxProviderMessageRefBytes) return normalized
  logger.warn('channel provider message reference omitted', {
    delivery_id: delivery.id,
    provider_message_ref_bytes: byteLength,
  })
  return ''
}

function requiredConfigurationRevision(value: number | undefined, kind: string): number {
  if (value !== undefined && Number.isSafeInteger(value) && value > 0) return value
  throw new ProviderDeliveryError(
    `claimed delivery is missing its ${kind} configuration revision`,
    {
      retryAfterMs: 1_000,
      retryable: true,
    },
  )
}

function retryDelay(
  delivery: ChannelConnectorDelivery,
  error: unknown,
  random: () => number = Math.random,
): number {
  if (error instanceof ProviderDeliveryError && error.retryAfterMs !== undefined) {
    return Math.min(Math.max(error.retryAfterMs, 100), 300_000)
  }
  const ceiling = Math.min(500 * 2 ** Math.max(delivery.attempt_count - 1, 0), 60_000)
  const fraction = Math.min(Math.max(random(), 0), 1)
  return Math.floor(ceiling / 2 + (ceiling / 2) * fraction)
}

function isAborted(signal: AbortSignal): boolean {
  return signal.aborted
}

async function acquireRuntimeHandle(
  registry: AppRuntimeRegistry,
  integrationAppId: string,
  expectedRevision: number,
  signal: AbortSignal,
): ReturnType<AppRuntimeRegistry['acquire']> {
  if (signal.aborted) throw abortReason(signal)
  const acquisition = registry.acquire(integrationAppId, expectedRevision)
  try {
    return await raceWithAbort(acquisition, signal)
  } catch (error) {
    void acquisition.then(
      (handle) => handle.release().catch(() => undefined),
      () => undefined,
    )
    throw error
  }
}

function remainingSendTimeMs(
  configuredTimeoutMs: number,
  completionTimeoutMs: number,
  localLeaseDeadlineMs: number,
  leaseMs: number,
): number {
  return Math.min(
    configuredTimeoutMs,
    Math.floor(
      localLeaseDeadlineMs - Date.now() - completionTimeoutMs - deliverySafetyMarginMs(leaseMs),
    ),
  )
}

function remainingCompletionTimeMs(
  localLeaseDeadlineMs: number,
  options: Pick<DeliveryLoopOptions, 'completionTimeoutMs' | 'leaseMs'>,
): number {
  return Math.max(
    1,
    Math.min(
      options.completionTimeoutMs,
      Math.floor(localLeaseDeadlineMs - Date.now() - deliverySafetyMarginMs(options.leaseMs)),
    ),
  )
}

function preparationExpired(
  signal: AbortSignal,
  sendDeadlineMs: number,
  localLeaseDeadlineMs: number,
  options: Pick<DeliveryLoopOptions, 'completionTimeoutMs' | 'leaseMs' | 'sendTimeoutMs'>,
): boolean {
  return (
    signal.aborted ||
    Date.now() >= sendDeadlineMs ||
    remainingSendTimeMs(
      options.sendTimeoutMs,
      options.completionTimeoutMs,
      localLeaseDeadlineMs,
      options.leaseMs,
    ) <= 0
  )
}

async function raceWithAbort<T>(work: Promise<T>, signal: AbortSignal): Promise<T> {
  if (signal.aborted) throw asError(signal.reason, 'channel delivery timed out')
  return new Promise<T>((resolve, reject) => {
    const onAbort = (): void => {
      reject(asError(signal.reason, 'channel delivery timed out'))
    }
    signal.addEventListener('abort', onAbort, { once: true })
    work.then(
      (value) => {
        signal.removeEventListener('abort', onAbort)
        resolve(value)
      },
      (error: unknown) => {
        signal.removeEventListener('abort', onAbort)
        reject(asError(error, 'channel delivery failed'))
      },
    )
  })
}

function asError(value: unknown, fallback: string): Error {
  if (value instanceof Error) return value
  if (typeof value === 'string') return new Error(value)
  return new Error(fallback)
}

function abortReason(signal: AbortSignal): Error {
  return asError(signal.reason, 'channel delivery was canceled')
}
