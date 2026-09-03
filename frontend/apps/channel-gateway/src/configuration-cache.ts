import type { ChannelConnectorInstallationConfiguration } from '@omnara/sdk'

import { type CoreClient, isCoreNotFoundError } from './core-client'
import { ProviderDeliveryError } from './types'

interface CachedInstallation {
  configuration: ChannelConnectorInstallationConfiguration
  externalKey: string
  fetchedAt: number
  loadSequence: number
}

interface CachedInstallationAlias {
  canonicalKey: string
  loadSequence: number
}

export interface NegativeCacheEntry {
  error: Error
  expiresAt: number
  loadSequence?: number
}

interface InstallationConfigurationCacheOptions {
  client: CoreClient
  limiter: LoadLimiter
  maxEntries: number
  notFoundCacheMs: number
  refreshAfterMs: number
}

export class InstallationConfigurationCache {
  // Full configurations are stored once under their immutable app/install ID.
  // External provider identities are lightweight aliases into this canonical LRU.
  private readonly aliases = new Map<string, CachedInstallationAlias>()
  private readonly entries = new Map<string, CachedInstallation>()
  private readonly loads = new Map<string, Promise<ChannelConnectorInstallationConfiguration>>()
  private readonly notFound = new Map<string, NegativeCacheEntry>()
  private closed = false
  private loadSequence = 0

  constructor(private readonly options: InstallationConfigurationCacheOptions) {}

  getByID(
    appId: string,
    installId: string,
    expectedAppRevision: number,
    expectedInstallRevision?: number,
  ): Promise<ChannelConnectorInstallationConfiguration> {
    const key = installationKey(appId, installId)
    return this.get(
      key,
      key,
      expectedAppRevision,
      expectedInstallRevision,
      false,
      (configuration) =>
        configuration.integration_app_id === appId && configuration.install.id === installId,
      () => this.options.client.getInstallationConfiguration(appId, installId),
    )
  }

  resolve(
    appId: string,
    externalTenantId: string,
    externalAccountRef: string,
    expectedAppRevision: number,
  ): Promise<ChannelConnectorInstallationConfiguration> {
    const key = externalKey(appId, externalTenantId, externalAccountRef)
    return this.get(
      key,
      this.aliases.get(key)?.canonicalKey,
      expectedAppRevision,
      undefined,
      true,
      (configuration) =>
        configuration.integration_app_id === appId &&
        configuration.install.provider_tenant_id === externalTenantId &&
        configuration.install.provider_account_ref === externalAccountRef,
      () =>
        this.options.client.resolveInstallationConfiguration(
          appId,
          externalTenantId,
          externalAccountRef,
        ),
    )
  }

  async close(): Promise<void> {
    this.closed = true
    await Promise.allSettled(this.loads.values())
    this.aliases.clear()
    this.entries.clear()
    this.notFound.clear()
  }

  private async get(
    lookupKey: string,
    canonicalKey: string | undefined,
    expectedAppRevision: number,
    expectedInstallRevision: number | undefined,
    mayReassignExternalAlias: boolean,
    matchesRequestedIdentity: (configuration: ChannelConnectorInstallationConfiguration) => boolean,
    fetchConfiguration: () => Promise<ChannelConnectorInstallationConfiguration>,
  ): Promise<ChannelConnectorInstallationConfiguration> {
    if (this.closed) throw new Error('channel installation configuration cache is closed')
    const cachedError = readNegativeCache(this.notFound, lookupKey)
    if (cachedError) throw cachedError
    const current = canonicalKey ? this.entries.get(canonicalKey) : undefined
    if (canonicalKey && !current && this.aliases.get(lookupKey)?.canonicalKey === canonicalKey) {
      this.aliases.delete(lookupKey)
    }
    if (
      current &&
      Date.now() - current.fetchedAt < this.options.refreshAfterMs &&
      configurationMatches(current.configuration, expectedAppRevision, expectedInstallRevision) &&
      matchesRequestedIdentity(current.configuration)
    ) {
      this.touch(canonicalKey ?? installationKeyFor(current.configuration), current)
      return current.configuration
    }
    const existingLoad = this.loads.get(lookupKey)
    if (existingLoad) {
      return validateInstallationConfiguration(
        await existingLoad,
        expectedAppRevision,
        expectedInstallRevision,
      )
    }
    if (!current && this.loads.size >= this.options.maxEntries) {
      throw new Error('channel installation configuration cache is at capacity')
    }
    const loadSequence = ++this.loadSequence
    const load = this.options.limiter
      .run(fetchConfiguration)
      .then((configuration) => {
        if (!matchesRequestedIdentity(configuration)) {
          throw new Error('core API returned a mismatched installation configuration')
        }
        validateInstallationConfiguration(
          configuration,
          expectedAppRevision,
          expectedInstallRevision,
        )
        const committed = this.put(configuration, loadSequence, mayReassignExternalAlias)
        const selected = this.configurationForLookup(lookupKey, canonicalKey) ?? committed
        if (!matchesRequestedIdentity(selected)) {
          throw new ProviderDeliveryError(
            'channel installation configuration changed during lookup',
            { retryAfterMs: 100, retryable: true },
          )
        }
        return selected
      })
      .catch((error: unknown) => {
        if (
          isCoreNotFoundError(error) &&
          !this.hasNewerMatchingEntry(
            canonicalKey,
            lookupKey,
            loadSequence,
            matchesRequestedIdentity,
          )
        ) {
          writeNegativeCache(
            this.notFound,
            lookupKey,
            error,
            this.options.notFoundCacheMs,
            this.options.maxEntries,
            loadSequence,
          )
        }
        throw error
      })
      .finally(() => {
        if (this.loads.get(lookupKey) === load) this.loads.delete(lookupKey)
      })
    this.loads.set(lookupKey, load)
    return validateInstallationConfiguration(
      await load,
      expectedAppRevision,
      expectedInstallRevision,
    )
  }

  private hasNewerMatchingEntry(
    canonicalKey: string | undefined,
    lookupKey: string,
    loadSequence: number,
    matchesRequestedIdentity: (configuration: ChannelConnectorInstallationConfiguration) => boolean,
  ): boolean {
    const currentKey = this.aliases.get(lookupKey)?.canonicalKey ?? canonicalKey
    const current = currentKey ? this.entries.get(currentKey) : undefined
    return (
      current !== undefined &&
      current.loadSequence > loadSequence &&
      matchesRequestedIdentity(current.configuration)
    )
  }

  private put(
    configuration: ChannelConnectorInstallationConfiguration,
    loadSequence: number,
    mayReassignExternalAlias: boolean,
  ): ChannelConnectorInstallationConfiguration {
    const key = installationKeyFor(configuration)
    const alias = externalKeyFor(configuration)
    const previous = this.entries.get(key)
    if (previous) {
      const versionOrder = compareConfigurationVersions(configuration, previous.configuration)
      if (versionOrder < 0) {
        this.setAlias(previous.externalKey, key, loadSequence, mayReassignExternalAlias)
        this.clearNegative(key, loadSequence)
        return previous.configuration
      }
      if (versionOrder === 0) {
        previous.fetchedAt = Date.now()
        previous.loadSequence = Math.max(previous.loadSequence, loadSequence)
        this.touch(key, previous)
        this.setAlias(previous.externalKey, key, loadSequence, mayReassignExternalAlias)
        this.clearNegative(key, loadSequence)
        return previous.configuration
      }
    }
    if (
      previous &&
      previous.externalKey !== alias &&
      this.aliases.get(previous.externalKey)?.canonicalKey === key
    ) {
      this.aliases.delete(previous.externalKey)
    }
    this.entries.delete(key)
    this.entries.set(key, {
      configuration,
      externalKey: alias,
      fetchedAt: Date.now(),
      loadSequence,
    })
    this.setAlias(alias, key, loadSequence, mayReassignExternalAlias)
    this.clearNegative(key, loadSequence)
    while (this.entries.size > this.options.maxEntries) {
      const oldest = this.entries.keys().next().value
      if (oldest === undefined) break
      const evicted = this.entries.get(oldest)
      this.entries.delete(oldest)
      if (evicted && this.aliases.get(evicted.externalKey)?.canonicalKey === oldest) {
        this.aliases.delete(evicted.externalKey)
      }
    }
    return configuration
  }

  private configurationForLookup(
    lookupKey: string,
    canonicalKey: string | undefined,
  ): ChannelConnectorInstallationConfiguration | undefined {
    const currentKey = this.aliases.get(lookupKey)?.canonicalKey ?? canonicalKey
    return currentKey ? this.entries.get(currentKey)?.configuration : undefined
  }

  private setAlias(
    alias: string,
    canonicalKey: string,
    loadSequence: number,
    mayReassign: boolean,
  ): void {
    const current = this.aliases.get(alias)
    if (current && current.canonicalKey !== canonicalKey && !mayReassign) return
    // Only the external-identity resolver is authoritative when an address has
    // moved to a different installation. Exact-ID loads may populate an empty
    // alias, but must neither reclaim nor block reassignment of an existing one.
    if (current?.canonicalKey === canonicalKey && current.loadSequence > loadSequence) {
      return
    }
    this.aliases.delete(alias)
    this.aliases.set(alias, { canonicalKey, loadSequence })
    this.clearNegative(alias, loadSequence)
  }

  private clearNegative(key: string, loadSequence: number): void {
    const current = this.notFound.get(key)
    if (current?.loadSequence !== undefined && current.loadSequence > loadSequence) return
    this.notFound.delete(key)
  }

  private touch(key: string, entry: CachedInstallation): void {
    if (this.entries.get(key) !== entry) return
    this.entries.delete(key)
    this.entries.set(key, entry)
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
  loadSequence?: number,
): void {
  cache.delete(key)
  cache.set(key, {
    error,
    expiresAt: Date.now() + ttlMs,
    ...(loadSequence === undefined ? {} : { loadSequence }),
  })
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

function configurationMatches(
  configuration: ChannelConnectorInstallationConfiguration,
  expectedAppRevision: number,
  expectedInstallRevision?: number,
): boolean {
  return (
    configuration.app_configuration_revision === expectedAppRevision &&
    (expectedInstallRevision === undefined ||
      configuration.install.configuration_revision === expectedInstallRevision)
  )
}

function validateInstallationConfiguration(
  configuration: ChannelConnectorInstallationConfiguration,
  expectedAppRevision: number,
  expectedInstallRevision?: number,
): ChannelConnectorInstallationConfiguration {
  if (configuration.app_configuration_revision !== expectedAppRevision) {
    throw staleConfigurationError(
      'app',
      expectedAppRevision,
      configuration.app_configuration_revision,
    )
  }
  if (
    expectedInstallRevision !== undefined &&
    configuration.install.configuration_revision !== expectedInstallRevision
  ) {
    throw staleConfigurationError(
      'installation',
      expectedInstallRevision,
      configuration.install.configuration_revision,
    )
  }
  return configuration
}

function compareConfigurationVersions(
  left: ChannelConnectorInstallationConfiguration,
  right: ChannelConnectorInstallationConfiguration,
): number {
  const appRevision = left.app_configuration_revision - right.app_configuration_revision
  if (appRevision !== 0) return appRevision
  return left.install.configuration_revision - right.install.configuration_revision
}

function cacheKey(...parts: string[]): string {
  return JSON.stringify(parts)
}

function installationKey(appId: string, installId: string): string {
  return cacheKey('id', appId, installId)
}

function installationKeyFor(configuration: ChannelConnectorInstallationConfiguration): string {
  return installationKey(configuration.integration_app_id, configuration.install.id)
}

function externalKey(appId: string, tenantId: string, accountRef: string): string {
  return cacheKey('external', appId, tenantId, accountRef)
}

function externalKeyFor(configuration: ChannelConnectorInstallationConfiguration): string {
  return externalKey(
    configuration.integration_app_id,
    configuration.install.provider_tenant_id,
    configuration.install.provider_account_ref,
  )
}
