import { realpathSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { AppRuntimeRegistry } from './app-registry'
import { RedisAppStateFactory } from './app-state'
import { asError } from './async'
import { type GatewayConfig, loadConfig } from './config'
import { CoreClient } from './core-client'
import { errorMessage } from './diagnostics'
import { builtInProviderFactories } from './factories'
import { JsonLogger } from './logger'
import { ReceiptConsumer, type ReceiptConsumerOptions } from './receipt-consumer'
import { createGatewayRedisClient, type GatewayRedisClient } from './redis-client'
import { RuntimeLoop } from './runtime-loop'
import { GatewayServer, type GatewayServerOptions } from './server'
import { createSlackGateway, slackCapability } from './slack/gateway'
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
  createRedisClient?: typeof createGatewayRedisClient
  factories?: ProviderFactory[]
  logger?: GatewayLogger
  operations?: GatewayServerOptions['operations']
  /** Explicit behavior and exact capabilities; independent of webhook factories. */
  receipts?: Omit<ReceiptConsumerOptions, 'client' | 'workBudget' | 'logger'>
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
    if (factories.size > 0) {
      if (!config.redisUrl || !config.redisTopology) {
        throw new Error(
          'OMNARA_CHANNEL_REDIS_URL and OMNARA_CHANNEL_REDIS_TOPOLOGY are required when provider factories are enabled',
        )
      }
      const startupDeadline = Date.now() + config.startupTimeoutMs
      redis = (options.createRedisClient ?? createGatewayRedisClient)({
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
    }

    const client = new CoreClient({
      baseUrl: config.apiBaseUrl,
      requestTimeoutMs: config.coreRequestTimeoutMs,
      token: config.connectorToken,
    })
    const workBudget = new WorkByteBudget(config.webhookMaxBufferedBytes)
    const slack = createSlackGateway({ core: client, workBudget })
    const receipts = new ReceiptConsumer({
      capabilities: [slackCapability],
      behavior: slack.processReceipt,
      maxConcurrentEvents: 4,
      maxAttempts: 8,
      leaseMs: 60_000,
      claimTimeoutMs: Math.min(config.coreRequestTimeoutMs, 10_000),
      behaviorTimeoutMs: 40_000,
      completionTimeoutMs: 5_000,
      idlePollMs: config.idlePollMs,
      ...options.receipts,
      client,
      workBudget,
      logger,
    })
    if (redis)
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
      isReady: () => factories.size === 0 || (redis?.ready() ?? false),
      logger,
      maxConcurrentRequests: config.webhookMaxConcurrentRequests,
      operations: options.operations ?? {
        credential: config.connectorToken,
        allowedCapabilities: [slackCapability],
        maxConcurrentRequests: config.operationMaxConcurrentRequests,
        maxTemporaryBytes: config.operationMaxTemporaryBytes,
        maxRequestBytes: config.operationMaxRequestBytes,
        maxDurationMs: config.operationMaxDurationMs,
        temporaryDirectory: config.operationTemporaryDirectory,
        execute: slack.executeOperation,
      },
      port: config.port,
      publicUrl: config.publicUrl,
      registry,
      workBudget,
    })
    const runtimeLoop = registry
      ? new RuntimeLoop({
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
      : undefined
    await server.listen()
    logger.info('channel gateway started', {
      port: config.port,
      provider_factories: factories.size,
    })
    const running = runtimeLoop ? [runtimeLoop.run(controller.signal)] : []
    running.push(receipts.run(controller.signal))
    loops = Promise.all(running).then(() => undefined)
    await Promise.race([abortPromise(controller.signal), loops])
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
  cleanup: () => Promise<void>,
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
      (cause: unknown) => {
        settle(() => {
          reject(asError(cause))
        })
      },
    )
    if (signal.aborted) onAbort()
  })
}

function signalError(signal: AbortSignal): Error {
  return signal.reason instanceof Error ? signal.reason : new Error('channel gateway stopping')
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
  runGateway().catch((cause: unknown) => {
    process.stderr.write(`${cause instanceof Error ? cause.stack : String(cause)}\n`)
    process.exitCode = 1
  })
}

export { normalizeProviderDeliveryError } from './chat-sdk-errors'
export type { ChatSdkAttachmentDataLoader, ChatSdkAttachmentLoadContext } from './chat-sdk-media'
export { fetchBoundedMedia, messageContentBlocks } from './chat-sdk-media'
export type { ReceiptConsumerOptions } from './receipt-consumer'
export { ReceiptConsumer } from './receipt-consumer'
export type {
  GatewayAppConfiguration,
  ProviderFactory,
  ProviderFactoryContext,
  ProviderRuntime,
  ProviderWebhookContext,
  ProviderWorkReservation,
  ReceiptBehavior,
  ReceiptBehaviorContext,
  RuntimeUnitContext,
} from './types'
export { ReceiptBehaviorError } from './types'
