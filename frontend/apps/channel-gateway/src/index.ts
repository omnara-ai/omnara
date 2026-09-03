import { realpathSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { AppRuntimeRegistry } from './app-registry'
import { RedisAppStateFactory } from './app-state'
import { type GatewayConfig, loadConfig } from './config'
import { CoreClient } from './core-client'
import { DeliveryLoop } from './delivery-loop'
import { errorMessage } from './diagnostics'
import { builtInProviderFactories } from './factories'
import { JsonLogger } from './logger'
import { createGatewayRedisClient, type GatewayRedisClient } from './redis-client'
import { RuntimeLoop } from './runtime-loop'
import { GatewayServer } from './server'
import {
  type GatewayLogger,
  type ProviderCapability,
  type ProviderFactory,
  providerFactoryKey,
  type ProviderFactoryRegistry,
} from './types'
import { WorkByteBudget } from './work-budget'

export interface RunGatewayOptions {
  config?: GatewayConfig
  factories?: ProviderFactory[]
  logger?: GatewayLogger
  signal?: AbortSignal
}

export async function runGateway(options: RunGatewayOptions = {}): Promise<void> {
  const config = options.config ?? loadConfig()
  const logger = options.logger ?? new JsonLogger()
  const factories = createProviderFactoryRegistry(options.factories ?? builtInProviderFactories())
  const capabilities = providerFactoryCapabilities(factories)
  const controller = new AbortController()
  const forwardAbort = () => {
    controller.abort(options.signal?.reason)
  }
  const stop = () => {
    controller.abort(new Error('channel gateway stopping'))
  }
  if (options.signal?.aborted) forwardAbort()
  options.signal?.addEventListener('abort', forwardAbort, { once: true })
  process.once('SIGINT', stop)
  process.once('SIGTERM', stop)
  let redis: GatewayRedisClient | undefined
  let registry: AppRuntimeRegistry | undefined
  let server: GatewayServer | undefined
  let loops: Promise<void> | undefined
  let redisConnected = false
  try {
    const startupDeadline = Date.now() + config.startupTimeoutMs
    redis = createGatewayRedisClient({
      clusterUrls: config.redisClusterUrls,
      socketTimeoutMs: config.redisSocketTimeoutMs,
      topology: config.redisTopology,
      url: config.redisUrl,
    })
    redis.onError((error: Error) => {
      logger.error('channel gateway Redis error', { error: error.message })
    })
    await runStartupStep(
      redis.connect(),
      controller.signal,
      startupDeadline,
      'connect channel gateway Redis',
    )
    redisConnected = true

    const client = new CoreClient({
      baseUrl: config.apiBaseUrl,
      requestTimeoutMs: config.coreRequestTimeoutMs,
      token: config.connectorToken,
    })
    const workBudget = new WorkByteBudget(config.webhookMaxBufferedBytes)
    registry = new AppRuntimeRegistry({
      client,
      factories,
      logger,
      maxApps: config.maxApps,
      maxConcurrentLoads: config.maxConcurrentLoads,
      maxInstallations: config.maxInstallations,
      notFoundCacheMs: config.notFoundCacheMs,
      providerLifecycleTimeoutMs: config.providerLifecycleTimeoutMs,
      reserveWorkBytes: workBudget.reserve,
      refreshAfterMs: config.refreshAfterMs,
      state: new RedisAppStateFactory({
        client: redis,
        keyPrefix: 'omnara:chat-sdk',
      }),
    })
    server = new GatewayServer({
      bodyLimitBytes: config.webhookBodyLimitBytes,
      handlerTimeoutMs: config.webhookHandlerTimeoutMs,
      httpShutdownTimeoutMs: config.httpShutdownTimeoutMs,
      isReady: () => redis?.ready() ?? false,
      logger,
      maxConcurrentRequests: config.webhookMaxConcurrentRequests,
      port: config.port,
      publicUrl: config.publicUrl,
      registry,
      workBudget,
    })
    const deliveryLoop = new DeliveryLoop({
      capabilities,
      claimLimit: config.deliveryClaimLimit,
      client,
      completionTimeoutMs: config.deliveryCompletionTimeoutMs,
      idlePollMs: config.idlePollMs,
      leaseMs: config.deliveryLeaseMs,
      logger,
      owner: config.instanceId,
      registry,
      sendTimeoutMs: config.deliverySendTimeoutMs,
    })
    const runtimeLoop = new RuntimeLoop({
      capabilities,
      claimLimit: config.runtimeClaimLimit,
      client,
      idlePollMs: config.idlePollMs,
      leaseMs: config.runtimeLeaseMs,
      logger,
      owner: config.instanceId,
      reserveWorkBytes: workBudget.reserve,
      registry,
      stopTimeoutMs: config.runtimeStopTimeoutMs,
    })
    await server.listen()
    logger.info('channel gateway started', {
      port: config.port,
      provider_factories: factories.size,
    })
    if (capabilities.length > 0) {
      loops = Promise.all(
        startClaimLoops(factories, deliveryLoop, runtimeLoop, controller.signal),
      ).then(() => undefined)
      await Promise.race([abortPromise(controller.signal), loops])
    } else {
      await abortPromise(controller.signal)
    }
    controller.abort(new Error('channel gateway stopping'))
    await server.close()
    await loops
  } finally {
    controller.abort(new Error('channel gateway stopping'))
    options.signal?.removeEventListener('abort', forwardAbort)
    process.removeListener('SIGINT', stop)
    process.removeListener('SIGTERM', stop)
    if (loops) await Promise.allSettled([loops])
    if (server) {
      const resource = server
      await cleanupResource(logger, 'close channel gateway HTTP server', () => resource.close())
    }
    if (registry) {
      const resource = registry
      await cleanupResource(logger, 'close channel app registry', () => resource.close())
    }
    if (redis) {
      const resource = redis
      if (redisConnected) {
        await cleanupResource(logger, 'close channel gateway Redis client', () => resource.close())
      } else {
        try {
          resource.destroy()
        } catch (error) {
          logger.warn('destroy channel gateway Redis client', { error: errorMessage(error) })
        }
      }
    }
  }
}

async function cleanupResource(
  logger: GatewayLogger,
  operation: string,
  cleanup: () => Promise<unknown>,
): Promise<void> {
  try {
    await cleanup()
  } catch (error) {
    logger.warn(operation, { error: error instanceof Error ? error.message : String(error) })
  }
}

async function runStartupStep<T>(
  work: Promise<T>,
  signal: AbortSignal,
  deadline: number,
  operation: string,
): Promise<T> {
  if (signal.aborted) throw signalError(signal)
  const remainingMs = deadline - Date.now()
  if (remainingMs <= 0) throw new Error('channel gateway startup reached its deadline')
  return new Promise<T>((resolve, reject) => {
    let settled = false
    const settle = (callback: () => void): void => {
      if (settled) return
      settled = true
      clearTimeout(timeout)
      signal.removeEventListener('abort', onAbort)
      callback()
    }
    const onAbort = (): void => {
      settle(() => {
        reject(signalError(signal))
      })
    }
    const timeout = setTimeout(() => {
      settle(() => {
        reject(new Error(`${operation} reached the startup deadline`))
      })
    }, remainingMs)
    signal.addEventListener('abort', onAbort, { once: true })
    work.then(
      (value) => {
        settle(() => {
          resolve(value)
        })
      },
      (error: unknown) => {
        settle(() => {
          reject(asError(error))
        })
      },
    )
    if (signal.aborted) onAbort()
  })
}

function signalError(signal: AbortSignal): Error {
  return signal.reason instanceof Error ? signal.reason : new Error('channel gateway stopping')
}

function asError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error))
}

const registryNamePattern = /^[a-z0-9][a-z0-9_.-]{0,127}$/

export function createProviderFactoryRegistry(
  factories: ProviderFactory[],
): ProviderFactoryRegistry {
  if (factories.length > 64)
    throw new Error('at most 64 channel provider factories may be registered')
  const registry = new Map<string, ProviderFactory>()
  for (const factory of factories) {
    if (
      !registryNamePattern.test(factory.connectorKey) ||
      !registryNamePattern.test(factory.provider)
    ) {
      throw new Error('channel provider factories must use lowercase registry names')
    }
    if (factory.connectorKey.startsWith('native_')) {
      throw new Error('native channel connector keys cannot be delegated to the gateway')
    }
    const key = providerFactoryKey(factory.connectorKey, factory.provider)
    if (registry.has(key))
      throw new Error(
        `duplicate channel provider factory ${factory.connectorKey}/${factory.provider}`,
      )
    registry.set(key, factory)
  }
  return registry
}

export function providerFactoryCapabilities(
  factories: ProviderFactoryRegistry,
): ProviderCapability[] {
  return Array.from(factories.values(), ({ connectorKey, provider }) => ({
    connector_key: connectorKey,
    provider,
  }))
}

export function startClaimLoops(
  factories: ProviderFactoryRegistry,
  deliveryLoop: Pick<DeliveryLoop, 'run'>,
  runtimeLoop: Pick<RuntimeLoop, 'run'>,
  signal: AbortSignal,
): Promise<void>[] {
  if (factories.size === 0) return []
  return [deliveryLoop.run(signal), runtimeLoop.run(signal)]
}

function abortPromise(signal: AbortSignal): Promise<void> {
  if (signal.aborted) return Promise.resolve()
  return new Promise<void>((resolve) => {
    signal.addEventListener(
      'abort',
      () => {
        resolve()
      },
      { once: true },
    )
  })
}

const entrypoint = process.argv[1]
if (entrypoint && realpathSync(entrypoint) === realpathSync(fileURLToPath(import.meta.url))) {
  runGateway().catch((error: unknown) => {
    process.stderr.write(`${error instanceof Error ? error.stack : String(error)}\n`)
    process.exitCode = 1
  })
}

export { createChatSdkLogger } from './chat-sdk-logger'
export type {
  ChannelDeliveryDestination,
  ChannelInteractionPromptPayload,
  ChannelMessagePayload,
  ChatSdkAttachmentDataLoader,
  ChatSdkAttachmentLoadContext,
  ChatSdkDeliveryHandler,
  ChatSdkEnvelopeIdentity,
  ChatSdkInboundActions,
  ChatSdkRuntimeOptions,
} from './chat-sdk-runtime'
export {
  chatSdkDeliveryHandlerKey,
  createChatSdkRuntime,
  fetchBoundedMedia,
  parseChannelInteractionPromptDelivery,
  parseChannelMessageDelivery,
} from './chat-sdk-runtime'
export type {
  GatewayAppConfiguration,
  ProviderFactory,
  ProviderFactoryContext,
  ProviderRuntime,
  ProviderSendContext,
  ProviderWebhookContext,
  ProviderWorkReservation,
  RuntimeUnitContext,
} from './types'
