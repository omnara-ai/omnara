import type { ChannelConnectorInstallationConfiguration } from '@omnara/sdk'

import type { CoreClient } from '../core/client'
import { isCoreNotFoundError } from '../core/requests'
import { ProviderDeliveryError } from '../types'
import { GatewayAtCapacityError } from '../work-budget'

interface CachedInstallation {
  configuration: ChannelConnectorInstallationConfiguration
  fetchedAt: number
}

export interface NegativeCacheEntry {
  error: Error
  expiresAt: number
}

interface InstallationConfigurationCacheOptions {
  client: Pick<CoreClient, 'resolveInstallationConfiguration'>
  limiter: LoadLimiter
  maxEntries: number
  notFoundCacheMs: number
  refreshAfterMs: number
}

export class InstallationConfigurationCache {
  private readonly entries = new Map<string, CachedInstallation>()
  private readonly loads = new Map<string, Promise<ChannelConnectorInstallationConfiguration>>()
  private readonly notFound = new Map<string, NegativeCacheEntry>()
  private closed = false

  constructor(private readonly options: InstallationConfigurationCacheOptions) {}

  async resolve(
    appId: string,
    externalTenantId: string,
    externalAccountRef: string,
    expectedAppRevision: number,
  ): Promise<ChannelConnectorInstallationConfiguration> {
    if (this.closed) throw new Error('channel installation configuration cache is closed')
    const key = JSON.stringify([appId, externalTenantId, externalAccountRef])
    const cachedError = readNegativeCache(this.notFound, key)
    if (cachedError) throw cachedError
    const current = this.entries.get(key)
    if (
      current &&
      Date.now() - current.fetchedAt < this.options.refreshAfterMs &&
      current.configuration.app_configuration_revision === expectedAppRevision
    ) {
      this.entries.delete(key)
      this.entries.set(key, current)
      return current.configuration
    }
    const existingLoad = this.loads.get(key)
    if (existingLoad) {
      return validateInstallationConfiguration(await existingLoad, expectedAppRevision)
    }
    if (!current && this.loads.size >= this.options.maxEntries) {
      throw new GatewayAtCapacityError('channel installation configuration cache is at capacity')
    }
    // One lookup identity and one in-flight load per key: no exact-ID alias or
    // competing lookup can replace the resolved installation out of order.
    const load = this.options.limiter
      .run(() =>
        this.options.client.resolveInstallationConfiguration(
          appId,
          externalTenantId,
          externalAccountRef,
        ),
      )
      .then((configuration) => {
        if (
          configuration.integration_app_id !== appId ||
          (configuration.install.provider_tenant_id ?? '') !== externalTenantId ||
          configuration.install.provider_account_ref !== externalAccountRef
        ) {
          throw new Error('core API returned a mismatched installation configuration')
        }
        this.entries.delete(key)
        this.entries.set(key, { configuration, fetchedAt: Date.now() })
        this.notFound.delete(key)
        while (this.entries.size > this.options.maxEntries) {
          const oldest = this.entries.keys().next().value
          if (oldest === undefined) break
          this.entries.delete(oldest)
        }
        return configuration
      })
      .catch((cause: unknown) => {
        if (isCoreNotFoundError(cause)) {
          writeNegativeCache(
            this.notFound,
            key,
            cause,
            this.options.notFoundCacheMs,
            this.options.maxEntries,
          )
        }
        throw cause
      })
      .finally(() => {
        this.loads.delete(key)
      })
    this.loads.set(key, load)
    return validateInstallationConfiguration(await load, expectedAppRevision)
  }

  async close(): Promise<void> {
    this.closed = true
    await Promise.allSettled(this.loads.values())
    this.entries.clear()
    this.notFound.clear()
  }
}

export class LoadLimiter {
  private active = 0
  private closed = false
  private readonly waiters: LoadWaiter[] = []

  constructor(private readonly limit: number) {
    if (!Number.isSafeInteger(limit) || limit <= 0) {
      throw new Error('positive concurrent load limit is required')
    }
  }

  async run<T>(operation: () => Promise<T>, signal?: AbortSignal): Promise<T> {
    await this.acquire(signal)
    try {
      return await operation()
    } finally {
      this.release()
    }
  }

  close(): void {
    if (this.closed) return
    this.closed = true
    for (const waiter of this.waiters.splice(0)) {
      waiter.cleanup()
      waiter.reject(new Error('channel configuration load limiter is closed'))
    }
  }

  private async acquire(signal?: AbortSignal): Promise<void> {
    if (this.closed) throw new Error('channel configuration load limiter is closed')
    if (signal?.aborted) throw abortReason(signal)
    if (this.active < this.limit) {
      this.active += 1
      return
    }
    await new Promise<void>((resolve, reject) => {
      const abortSignal = signal
      const onAbort = (): void => {
        const index = this.waiters.indexOf(waiter)
        if (index >= 0) this.waiters.splice(index, 1)
        waiter.cleanup()
        reject(
          abortSignal
            ? abortReason(abortSignal)
            : new Error('configuration load aborted without an abort signal'),
        )
      }
      const waiter: LoadWaiter = {
        cleanup: () => abortSignal?.removeEventListener('abort', onAbort),
        reject,
        resolve,
      }
      abortSignal?.addEventListener('abort', onAbort, { once: true })
      this.waiters.push(waiter)
    })
  }

  private release(): void {
    const waiter = this.waiters.shift()
    if (waiter) {
      // The released permit transfers directly to the waiter. Keeping active
      // unchanged prevents a newly arriving load from stealing it first.
      waiter.cleanup()
      waiter.resolve()
      return
    }
    this.active -= 1
  }
}

interface LoadWaiter {
  cleanup: () => void
  reject: (error: Error) => void
  resolve: () => void
}

function abortReason(signal: AbortSignal): Error {
  return signal.reason instanceof Error ? signal.reason : new Error('configuration load aborted')
}

export function readNegativeCache(
  cache: Map<string, NegativeCacheEntry>,
  key: string,
): Error | undefined {
  const entry = cache.get(key)
  if (!entry) return undefined
  if (entry.expiresAt <= Date.now()) {
    cache.delete(key)
    return undefined
  }
  cache.delete(key)
  cache.set(key, entry)
  return entry.error
}

export function writeNegativeCache(
  cache: Map<string, NegativeCacheEntry>,
  key: string,
  error: Error,
  ttlMs: number,
  maximum: number,
): void {
  cache.delete(key)
  const entry: NegativeCacheEntry = { error, expiresAt: Date.now() + ttlMs }
  cache.set(key, entry)
  while (cache.size > maximum) {
    const oldest = cache.keys().next().value
    if (oldest === undefined) return
    cache.delete(oldest)
  }
}

export function staleConfigurationError(
  kind: string,
  expectedRevision: number,
  actualRevision: number,
): ProviderDeliveryError {
  return new ProviderDeliveryError(
    `channel ${kind} configuration revision changed from ${expectedRevision} to ${actualRevision}`,
    { retryAfterMs: 100, retryable: true },
  )
}

function validateInstallationConfiguration(
  configuration: ChannelConnectorInstallationConfiguration,
  expectedAppRevision: number,
): ChannelConnectorInstallationConfiguration {
  if (configuration.app_configuration_revision !== expectedAppRevision) {
    throw staleConfigurationError(
      'app',
      expectedAppRevision,
      configuration.app_configuration_revision,
    )
  }
  return configuration
}
