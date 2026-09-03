import type {
  ChannelConnectorInstallationConfiguration,
  ChannelConnectorRuntimeUnit,
} from '@omnara/sdk'

import type { AppStateFactory } from './app-state'
import {
  InstallationConfigurationCache,
  LoadLimiter,
  type NegativeCacheEntry,
  readNegativeCache,
  staleConfigurationError,
  writeNegativeCache,
} from './configuration-cache'
import { type CoreClient, isCoreNotFoundError } from './core-client'
import { errorMessage } from './diagnostics'
import {
  type GatewayAppConfiguration,
  type GatewayLogger,
  ProviderDeliveryError,
  type ProviderFactory,
  type ProviderFactoryContext,
  providerFactoryKey,
  type ProviderFactoryRegistry,
  type ProviderRuntime,
  type ProviderWebhookWorkContext,
  type RuntimeUnitWorkContext,
} from './types'

interface RuntimeEntry {
  clearSubscriptionsOnClose?: string
  closing?: Promise<void>
  configuration: GatewayAppConfiguration
  fetchedAt: number
  lifecycle: AbortController
  refs: number
  retired: boolean
  runtime: ProviderRuntime
}

interface AppSlotReservation {
  victim?: { entry: RuntimeEntry; id: string }
}

export interface RuntimeHandle {
  configuration: GatewayAppConfiguration
  getInstallation: (
    integrationInstallId: string,
    expectedRevision?: number,
  ) => Promise<ChannelConnectorInstallationConfiguration>
  handleWebhook: (request: Request, context: ProviderWebhookWorkContext) => Promise<Response>
  release: () => Promise<void>
  resolveInstallation: (
    externalTenantId: string,
    externalAccountRef: string,
  ) => Promise<ChannelConnectorInstallationConfiguration>
  runUnit: (unit: ChannelConnectorRuntimeUnit, context: RuntimeUnitWorkContext) => Promise<void>
  runtime: ProviderRuntime
}

export interface AppRuntimeRegistryOptions {
  client: CoreClient
  factories: ProviderFactoryRegistry
  logger: GatewayLogger
  maxApps: number
  maxConcurrentLoads: number
  maxInstallations: number
  notFoundCacheMs: number
  providerLifecycleTimeoutMs: number
  reserveWorkBytes: ProviderFactoryContext['reserveWorkBytes']
  refreshAfterMs: number
  state: AppStateFactory
}

export class AppRuntimeRegistry {
  private readonly entries = new Map<string, RuntimeEntry>()
  private readonly installations: InstallationConfigurationCache
  private readonly limiter: LoadLimiter
  private readonly loads = new Map<string, Promise<RuntimeEntry>>()
  private readonly notFound = new Map<string, NegativeCacheEntry>()
  private readonly reservedEntries = new Set<RuntimeEntry>()
  private closed = false
  private pendingNewLoads = 0

  constructor(private readonly options: AppRuntimeRegistryOptions) {
    this.limiter = new LoadLimiter(options.maxConcurrentLoads)
    this.installations = new InstallationConfigurationCache({
      client: options.client,
      limiter: this.limiter,
      maxEntries: options.maxInstallations,
      notFoundCacheMs: options.notFoundCacheMs,
      refreshAfterMs: options.refreshAfterMs,
    })
  }

  async acquire(integrationAppId: string, expectedRevision?: number): Promise<RuntimeHandle> {
    if (this.closed) throw new Error('channel app registry is closed')
    const cachedError = readNegativeCache(this.notFound, integrationAppId)
    if (cachedError !== undefined) throw cachedError

    const entry = await this.load(integrationAppId, expectedRevision)
    if (entry.retired) {
      if (entry.refs === 0) await this.closeEntry(entry)
      throw new Error('channel app registry is closed')
    }
    if (
      expectedRevision !== undefined &&
      entry.configuration.app.configuration_revision !== expectedRevision
    ) {
      throw staleConfigurationError(
        'app',
        expectedRevision,
        entry.configuration.app.configuration_revision,
      )
    }

    entry.refs += 1
    this.touchEntry(integrationAppId, entry)
    let released = false
    const appId = entry.configuration.app.id
    const appRevision = entry.configuration.app.configuration_revision
    const getInstallation = (integrationInstallId: string, installRevision?: number) =>
      this.installations.getByID(appId, integrationInstallId, appRevision, installRevision)

    return {
      configuration: entry.configuration,
      getInstallation,
      handleWebhook: (request, context) =>
        entry.runtime.handleWebhook(request, {
          ...context,
          resolveInteraction: (interactionId, interaction) =>
            this.options.client.resolveInteraction(
              appId,
              interactionId,
              interaction,
              request.signal,
            ),
          submitInbound: (event) => this.options.client.submitInbound(appId, event, request.signal),
        }),
      release: async () => {
        if (released) return
        released = true
        entry.refs -= 1
        if (entry.retired && entry.refs === 0) await this.closeEntry(entry)
      },
      resolveInstallation: (externalTenantId, externalAccountRef) =>
        this.installations.resolve(appId, externalTenantId, externalAccountRef, appRevision),
      runUnit: (unit, context) => {
        const runUnit = entry.runtime.runUnit
        if (!runUnit) {
          throw new ProviderDeliveryError('provider adapter does not support runtime units')
        }
        return runUnit(unit, {
          ...context,
          resolveInteraction: (interactionId, interaction) =>
            this.options.client.resolveRuntimeInteraction(
              appId,
              unit,
              interactionId,
              interaction,
              context.signal,
            ),
          submitInbound: (event) =>
            this.options.client.submitRuntimeInbound(appId, unit, event, context.signal),
        })
      },
      runtime: entry.runtime,
    }
  }

  async close(): Promise<void> {
    if (this.closed) return
    this.closed = true
    this.limiter.close()
    await Promise.allSettled(this.loads.values())
    const entries = [...this.entries.values()]
    this.entries.clear()
    await this.installations.close()
    this.notFound.clear()
    for (const entry of entries) entry.retired = true
    await Promise.all(
      entries.filter((entry) => entry.refs === 0).map((entry) => this.closeEntry(entry)),
    )
  }

  private async load(integrationAppId: string, expectedRevision?: number): Promise<RuntimeEntry> {
    const current = this.entries.get(integrationAppId)
    if (
      current &&
      !current.retired &&
      Date.now() - current.fetchedAt < this.options.refreshAfterMs &&
      (expectedRevision === undefined ||
        current.configuration.app.configuration_revision === expectedRevision)
    ) {
      return current
    }
    const existingLoad = this.loads.get(integrationAppId)
    if (existingLoad) return existingLoad
    const reservation = current ? undefined : this.reserveAppSlot()
    const load = this.limiter
      .run(() => this.refresh(integrationAppId, current, reservation))
      .catch(async (error: unknown) => {
        if (isCoreNotFoundError(error)) {
          if (current) {
            current.clearSubscriptionsOnClose = integrationAppId
            await this.retireEntry(integrationAppId, current)
          } else {
            await this.clearDeletedAppState(integrationAppId)
          }
          writeNegativeCache(
            this.notFound,
            integrationAppId,
            error,
            this.options.notFoundCacheMs,
            this.options.maxApps,
          )
        } else if (current?.retired) {
          await this.retireEntry(integrationAppId, current)
        }
        throw error
      })
      .finally(() => {
        if (reservation) this.releaseAppSlot(reservation)
        if (this.loads.get(integrationAppId) === load) this.loads.delete(integrationAppId)
      })
    this.loads.set(integrationAppId, load)
    return load
  }

  private async refresh(
    integrationAppId: string,
    current?: RuntimeEntry,
    reservation?: AppSlotReservation,
  ): Promise<RuntimeEntry> {
    const configuration = await this.options.client.getAppConfiguration(integrationAppId)
    this.notFound.delete(integrationAppId)
    if (configuration.app.id !== integrationAppId) {
      throw new Error('core API returned a mismatched integration app configuration')
    }
    await this.options.state.markKnownApp(integrationAppId)
    if (
      current &&
      !current.retired &&
      current.configuration.app.configuration_revision === configuration.app.configuration_revision
    ) {
      current.configuration = configuration
      current.fetchedAt = Date.now()
      return current
    }
    if (current) {
      current.retired = true
    }

    const key = providerFactoryKey(configuration.app.connector_key, configuration.app.provider)
    const factory = this.options.factories.get(key)
    if (!factory) {
      throw new ProviderDeliveryError(
        `no provider factory is registered for connector ${configuration.app.connector_key} and provider ${configuration.app.provider}`,
      )
    }
    const state = this.options.state.forApp(configuration.app.id)
    const appRevision = configuration.app.configuration_revision
    const created = await this.createRuntime(factory, {
      configuration,
      getInstallation: (integrationInstallId, expectedInstallRevision) =>
        this.installations.getByID(
          configuration.app.id,
          integrationInstallId,
          appRevision,
          expectedInstallRevision,
        ),
      logger: this.options.logger,
      reserveWorkBytes: this.options.reserveWorkBytes,
      resolveInstallation: (externalTenantId, externalAccountRef) =>
        this.installations.resolve(
          configuration.app.id,
          externalTenantId,
          externalAccountRef,
          appRevision,
        ),
      state,
    })
    const entry: RuntimeEntry = {
      configuration,
      fetchedAt: Date.now(),
      lifecycle: created.lifecycle,
      refs: 0,
      retired: false,
      runtime: created.runtime,
    }
    if (this.closed) {
      entry.retired = true
      await this.closeEntry(entry)
      throw new Error('channel app registry is closed')
    }
    let evicted: RuntimeEntry | undefined
    if (!current) {
      try {
        evicted = this.commitAppSlot(reservation)
      } catch (error) {
        entry.retired = true
        await this.closeEntry(entry)
        throw error
      }
    }
    this.entries.set(integrationAppId, entry)
    if (current) {
      current.retired = true
      if (current.refs === 0) await this.closeEntry(current)
    }
    if (evicted) await this.closeEntry(evicted)
    return entry
  }

  private async closeEntry(entry: RuntimeEntry): Promise<void> {
    entry.lifecycle.abort(new Error('provider runtime retired'))
    entry.closing ??= this.closeRuntime(entry.runtime)
    await entry.closing
    if (entry.clearSubscriptionsOnClose) {
      await this.clearDeletedAppState(entry.clearSubscriptionsOnClose)
      entry.clearSubscriptionsOnClose = undefined
    }
  }

  private async clearDeletedAppState(integrationAppId: string): Promise<void> {
    try {
      await this.options.state.clearSubscriptions(integrationAppId)
    } catch (error) {
      this.options.logger.warn('clear deleted provider app subscriptions', {
        error: errorMessage(error),
        integration_app_id: integrationAppId,
      })
    }
  }

  private async retireEntry(id: string, entry: RuntimeEntry): Promise<void> {
    if (this.entries.get(id) === entry) this.entries.delete(id)
    entry.retired = true
    if (entry.refs === 0) await this.closeEntry(entry)
  }

  private async createRuntime(
    factory: ProviderFactory,
    context: Omit<ProviderFactoryContext, 'signal'>,
  ): Promise<{ lifecycle: AbortController; runtime: ProviderRuntime }> {
    const controller = new AbortController()
    const timeout = setTimeout(() => {
      controller.abort(new Error('provider runtime creation reached its deadline'))
    }, this.options.providerLifecycleTimeoutMs)
    let creation: Promise<ProviderRuntime> | undefined
    try {
      creation = factory.create({ ...context, signal: controller.signal })
      const runtime = await raceWithSignal(creation, controller.signal)
      return { lifecycle: controller, runtime }
    } catch (error) {
      controller.abort(error)
      if (creation) {
        void creation.then(
          (runtime) => this.closeRuntime(runtime),
          () => undefined,
        )
      }
      throw error
    } finally {
      clearTimeout(timeout)
    }
  }

  private async closeRuntime(runtime: ProviderRuntime): Promise<void> {
    let timeout: ReturnType<typeof setTimeout> | undefined
    try {
      await Promise.race([
        runtime.close(),
        new Promise<never>((_resolve, reject) => {
          timeout = setTimeout(() => {
            reject(new Error('provider runtime close reached its deadline'))
          }, this.options.providerLifecycleTimeoutMs)
        }),
      ])
    } catch (error) {
      this.options.logger.error('close provider runtime', { error: errorMessage(error) })
    } finally {
      if (timeout) clearTimeout(timeout)
    }
  }

  private reserveAppSlot(): AppSlotReservation {
    if (this.entries.size + this.pendingNewLoads < this.options.maxApps) {
      this.pendingNewLoads += 1
      return {}
    }
    for (const [id, entry] of this.entries) {
      if (entry.refs > 0 || this.loads.has(id) || this.reservedEntries.has(entry)) continue
      this.reservedEntries.add(entry)
      this.pendingNewLoads += 1
      return { victim: { entry, id } }
    }
    throw new Error('channel app registry is at capacity')
  }

  private commitAppSlot(reservation: AppSlotReservation | undefined): RuntimeEntry | undefined {
    if (!reservation) throw new Error('channel app registry slot was not reserved')
    if (this.entries.size < this.options.maxApps) return undefined

    let victim = reservation.victim
    if (!victim || !this.canEvict(victim.id, victim.entry)) {
      victim = undefined
      for (const [id, entry] of this.entries) {
        if (this.reservedEntries.has(entry) || !this.canEvict(id, entry)) continue
        victim = { entry, id }
        break
      }
    }
    if (!victim) throw new Error('channel app registry is at capacity')
    this.entries.delete(victim.id)
    victim.entry.retired = true
    return victim.entry
  }

  private canEvict(id: string, entry: RuntimeEntry): boolean {
    return this.entries.get(id) === entry && entry.refs === 0 && !this.loads.has(id)
  }

  private releaseAppSlot(reservation: AppSlotReservation): void {
    if (reservation.victim) this.reservedEntries.delete(reservation.victim.entry)
    this.pendingNewLoads -= 1
  }

  private touchEntry(id: string, entry: RuntimeEntry): void {
    if (this.entries.get(id) !== entry) return
    this.entries.delete(id)
    this.entries.set(id, entry)
  }
}

async function raceWithSignal<T>(work: Promise<T>, signal: AbortSignal): Promise<T> {
  if (signal.aborted) throw signalError(signal)
  return new Promise<T>((resolve, reject) => {
    const onAbort = (): void => {
      reject(signalError(signal))
    }
    signal.addEventListener('abort', onAbort, { once: true })
    work.then(
      (value) => {
        signal.removeEventListener('abort', onAbort)
        resolve(value)
      },
      (error: unknown) => {
        signal.removeEventListener('abort', onAbort)
        reject(error instanceof Error ? error : new Error(String(error)))
      },
    )
  })
}

function signalError(signal: AbortSignal): Error {
  return signal.reason instanceof Error ? signal.reason : new Error('provider lifecycle aborted')
}
