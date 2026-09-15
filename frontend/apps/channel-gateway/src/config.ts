import { tmpdir } from 'node:os'

const instanceIdPattern = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,126}$/

export type RedisTopology = 'cluster' | 'standalone'

export interface GatewayConfig {
  apiBaseUrl: string
  connectorToken: string
  coreRequestTimeoutMs: number
  operationMaxDurationMs: number
  operationMaxConcurrentRequests: number
  operationMaxRequestBytes: number
  operationMaxTemporaryBytes: number
  operationTemporaryDirectory: string
  idlePollMs: number
  httpShutdownTimeoutMs: number
  instanceId: string
  maxApps: number
  maxConcurrentLoads: number
  maxInstallations: number
  notFoundCacheMs: number
  port: number
  providerLifecycleTimeoutMs: number
  publicUrl: string
  redisClusterUrls: string[]
  redisSocketTimeoutMs: number
  redisTopology?: RedisTopology
  redisUrl?: string
  refreshAfterMs: number
  runtimeClaimLimit: number
  runtimeLeaseMs: number
  runtimeStopTimeoutMs: number
  startupTimeoutMs: number
  webhookBodyLimitBytes: number
  webhookHandlerTimeoutMs: number
  webhookMaxBufferedBytes: number
  webhookMaxConcurrentRequests: number
}

export function loadConfig(env: NodeJS.ProcessEnv = process.env): GatewayConfig {
  const instanceId = nonEmpty(env.OMNARA_CHANNEL_GATEWAY_INSTANCE_ID) ?? crypto.randomUUID()
  if (!instanceIdPattern.test(instanceId)) {
    throw new Error(
      'OMNARA_CHANNEL_GATEWAY_INSTANCE_ID must be 1-127 ASCII letters, digits, dots, colons, underscores, or hyphens',
    )
  }
  const redisUrl = nonEmpty(env.OMNARA_CHANNEL_REDIS_URL)
    ? requiredRedisUrl(env.OMNARA_CHANNEL_REDIS_URL, 'OMNARA_CHANNEL_REDIS_URL')
    : undefined
  const redisClusterUrls = redisUrls(
    env.OMNARA_CHANNEL_REDIS_CLUSTER_URLS,
    'OMNARA_CHANNEL_REDIS_CLUSTER_URLS',
  )
  const redisTopology =
    redisUrl || redisClusterUrls.length > 0 || nonEmpty(env.OMNARA_CHANNEL_REDIS_TOPOLOGY)
      ? redisTopologyValue(env.OMNARA_CHANNEL_REDIS_TOPOLOGY)
      : undefined
  if (redisTopology === 'standalone' && redisClusterUrls.length > 0) {
    throw new Error(
      'OMNARA_CHANNEL_REDIS_CLUSTER_URLS requires OMNARA_CHANNEL_REDIS_TOPOLOGY=cluster',
    )
  }
  const redisSocketTimeoutMs = integer(
    env.OMNARA_CHANNEL_REDIS_SOCKET_TIMEOUT_MS,
    5_000,
    100,
    30_000,
  )
  const webhookHandlerTimeoutMs = integer(
    env.OMNARA_CHANNEL_WEBHOOK_HANDLER_TIMEOUT_MS,
    30_000,
    100,
    300_000,
  )
  if (redisTopology && redisSocketTimeoutMs > webhookHandlerTimeoutMs) {
    throw new Error(
      'OMNARA_CHANNEL_REDIS_SOCKET_TIMEOUT_MS must not exceed OMNARA_CHANNEL_WEBHOOK_HANDLER_TIMEOUT_MS',
    )
  }
  return {
    apiBaseUrl: requiredHttpUrl(env.OMNARA_CHANNEL_CORE_API_URL, 'OMNARA_CHANNEL_CORE_API_URL'),
    connectorToken: required(env.OMNARA_CHANNEL_CONNECTOR_TOKEN, 'OMNARA_CHANNEL_CONNECTOR_TOKEN'),
    coreRequestTimeoutMs: integer(env.OMNARA_CHANNEL_CORE_REQUEST_TIMEOUT_MS, 10_000, 100, 300_000),
    operationMaxDurationMs: integer(
      env.OMNARA_CHANNEL_OPERATION_MAX_DURATION_MS,
      300_000,
      1000,
      300_000,
    ),
    operationMaxConcurrentRequests: integer(
      env.OMNARA_CHANNEL_OPERATION_MAX_CONCURRENT_REQUESTS,
      8,
      1,
      1000,
    ),
    operationMaxRequestBytes: integer(
      env.OMNARA_CHANNEL_OPERATION_MAX_REQUEST_BYTES,
      1024 * 1024 * 1024,
      512 * 1024,
      Number.MAX_SAFE_INTEGER,
    ),
    operationMaxTemporaryBytes: integer(
      env.OMNARA_CHANNEL_OPERATION_MAX_TEMPORARY_BYTES,
      4 * 1024 * 1024 * 1024,
      1,
      Number.MAX_SAFE_INTEGER,
    ),
    operationTemporaryDirectory:
      nonEmpty(env.OMNARA_CHANNEL_OPERATION_TEMPORARY_DIRECTORY) ?? tmpdir(),
    idlePollMs: integer(env.OMNARA_CHANNEL_IDLE_POLL_MS, 1_000, 50, 60_000),
    httpShutdownTimeoutMs: integer(
      env.OMNARA_CHANNEL_HTTP_SHUTDOWN_TIMEOUT_MS,
      10_000,
      100,
      300_000,
    ),
    instanceId,
    maxApps: integer(env.OMNARA_CHANNEL_MAX_APPS, 1_000, 1, 100_000),
    maxConcurrentLoads: integer(env.OMNARA_CHANNEL_MAX_CONCURRENT_LOADS, 32, 1, 1_000),
    maxInstallations: integer(env.OMNARA_CHANNEL_MAX_INSTALLATIONS, 10_000, 1, 1_000_000),
    notFoundCacheMs: integer(env.OMNARA_CHANNEL_NOT_FOUND_CACHE_MS, 5_000, 100, 60_000),
    port: integer(env.PORT, 8080, 1, 65_535),
    providerLifecycleTimeoutMs: integer(
      env.OMNARA_CHANNEL_PROVIDER_LIFECYCLE_TIMEOUT_MS,
      10_000,
      100,
      300_000,
    ),
    publicUrl: requiredPublicUrl(
      env.OMNARA_CHANNEL_GATEWAY_PUBLIC_URL,
      'OMNARA_CHANNEL_GATEWAY_PUBLIC_URL',
    ),
    redisClusterUrls,
    redisSocketTimeoutMs,
    redisTopology,
    redisUrl,
    refreshAfterMs: integer(env.OMNARA_CHANNEL_APP_REFRESH_MS, 60_000, 1_000, 3_600_000),
    runtimeClaimLimit: integer(env.OMNARA_CHANNEL_RUNTIME_CLAIM_LIMIT, 32, 1, 1000),
    runtimeLeaseMs: integer(env.OMNARA_CHANNEL_RUNTIME_LEASE_MS, 30_000, 3_000, 300_000),
    runtimeStopTimeoutMs: integer(env.OMNARA_CHANNEL_RUNTIME_STOP_TIMEOUT_MS, 10_000, 100, 300_000),
    startupTimeoutMs: integer(env.OMNARA_CHANNEL_STARTUP_TIMEOUT_MS, 30_000, 100, 300_000),
    webhookBodyLimitBytes: integer(
      env.OMNARA_CHANNEL_WEBHOOK_BODY_LIMIT_BYTES,
      24 * 1024 * 1024,
      1,
      100 * 1024 * 1024,
    ),
    webhookHandlerTimeoutMs,
    webhookMaxBufferedBytes: integer(
      env.OMNARA_CHANNEL_WEBHOOK_MAX_BUFFERED_BYTES,
      128 * 1024 * 1024,
      1,
      1024 * 1024 * 1024,
    ),
    webhookMaxConcurrentRequests: integer(
      env.OMNARA_CHANNEL_WEBHOOK_MAX_CONCURRENT_REQUESTS,
      128,
      1,
      100_000,
    ),
  }
}

function redisTopologyValue(value: string | undefined): RedisTopology {
  const normalized = required(value, 'OMNARA_CHANNEL_REDIS_TOPOLOGY').toLowerCase()
  if (normalized !== 'standalone' && normalized !== 'cluster') {
    throw new Error('OMNARA_CHANNEL_REDIS_TOPOLOGY must be standalone or cluster')
  }
  return normalized
}

function redisUrls(value: string | undefined, name: string): string[] {
  if (!nonEmpty(value)) return []
  const urls = value?.split(',').map((entry) => requiredRedisUrl(entry, name)) ?? []
  if (urls.some((url, index) => urls.indexOf(url) !== index)) {
    throw new Error(`${name} must not contain duplicate URLs`)
  }
  return urls
}

function nonEmpty(value: string | undefined): string | undefined {
  const normalized = value?.trim()
  return normalized === '' ? undefined : normalized
}

function required(value: string | undefined, name: string): string {
  const normalized = value?.trim()
  if (!normalized) throw new Error(`${name} is required`)
  return normalized
}

function requiredHttpUrl(value: string | undefined, name: string): string {
  const url = parsedUrl(value, name)
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new Error(`${name} must use http or https`)
  }
  return url.href.replace(/\/+$/, '')
}

function requiredPublicUrl(value: string | undefined, name: string): string {
  const url = new URL(requiredHttpUrl(value, name))
  if (url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
    throw new Error(
      `${name} must be a public origin without credentials, a path, query, or fragment`,
    )
  }
  return url.origin
}

function requiredRedisUrl(value: string | undefined, name: string): string {
  const url = parsedUrl(value, name)
  if (url.protocol !== 'redis:' && url.protocol !== 'rediss:') {
    throw new Error(`${name} must use redis or rediss`)
  }
  return url.href.replace(/\/+$/, '')
}

function parsedUrl(value: string | undefined, name: string): URL {
  const normalized = required(value, name)
  try {
    return new URL(normalized)
  } catch {
    throw new Error(`${name} must be a valid URL`)
  }
}

function integer(
  value: string | undefined,
  fallback: number,
  minimum: number,
  maximum: number,
): number {
  if (value === undefined || value.trim() === '') return fallback
  const parsed = Number(value)
  if (!Number.isSafeInteger(parsed) || parsed < minimum || parsed > maximum) {
    throw new Error(`integer configuration must be between ${minimum} and ${maximum}`)
  }
  return parsed
}
