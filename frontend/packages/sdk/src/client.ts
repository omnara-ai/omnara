import * as z from 'zod'

import type { AuthStrategy } from './auth'
import { ApiError } from './errors'
import type { Client as OmnaraClient, Config } from './generated/client'
import { createClient, createConfig, formDataBodySerializer } from './generated/client'
import { client as specDefaultClient } from './generated/client.gen'
import type { BodySerializer } from './generated/core/bodySerializer.gen'
import {
  captureRequestDestination,
  normalizeHeaders,
  requestWithPolicy,
  validateClientRequestPolicy,
} from './request-policy'
import type { RequestPolicyOptions } from './request-policy-options'

export type {
  Client as OmnaraClient,
  Config as OmnaraClientConfig,
  ClientOptions as OmnaraClientConfigOptions,
} from './generated/client'

export interface OmnaraClientOptions extends RequestPolicyOptions {
  baseUrl?: string
  credentials?: RequestCredentials
  auth?: AuthStrategy
  headers?: Config['headers']
  fetch?: typeof fetch
}

type HttpMethod =
  'connect' | 'delete' | 'get' | 'head' | 'options' | 'patch' | 'post' | 'put' | 'trace'

const httpMethods = [
  'connect',
  'delete',
  'get',
  'head',
  'options',
  'patch',
  'post',
  'put',
  'trace',
] satisfies HttpMethod[]

const zMultipartPart = z.union([
  z.string(),
  z.instanceof(Blob),
  z.date().transform((date) => date.toISOString()),
  z.union([z.number(), z.boolean(), z.bigint()]).transform(String),
  z.json().transform((value) => new Blob([JSON.stringify(value)], { type: 'application/json' })),
])

const zMultipartBody = z.record(
  z.string(),
  z.union([z.array(zMultipartPart), zMultipartPart]).nullish(),
)

const serializeMultipartBody = ((body) => {
  const form = new FormData()
  for (const [key, value] of Object.entries(zMultipartBody.parse(body))) {
    if (value === null || value === undefined) continue
    for (const part of Array.isArray(value) ? value : [value]) form.append(key, part)
  }
  return form
}) satisfies BodySerializer

function normalizeRequestOptions<O extends object>(options: O): O {
  const normalized: O & { bodySerializer?: unknown } = { ...options }
  Reflect.deleteProperty(normalized, 'client')
  if (normalized.bodySerializer === formDataBodySerializer.bodySerializer) {
    normalized.bodySerializer = serializeMultipartBody
  }
  return normalized
}

// Generated HTTP methods close over request(), so route them through our policy entry.
// Normalization also drops the reserved Request init key until hey-api#4177 is fixed.
function configureRequests(client: OmnaraClient): void {
  client.interceptors.request.use(captureRequestDestination)
  const { setConfig } = client
  client.setConfig = (config) => {
    validateClientRequestPolicy(config)
    return setConfig({ ...config, headers: normalizeHeaders(config.headers) })
  }
  const { request } = client
  client.request = (options) =>
    requestWithPolicy(client.getConfig(), normalizeRequestOptions(options), (overrides) =>
      request(normalizeRequestOptions({ ...options, ...overrides })),
    )
  for (const method of httpMethods) {
    // SAFETY: every entry is a generated HTTP method; casing preserves that union.
    const requestMethod = method.toUpperCase() as Uppercase<HttpMethod>
    client[method] = (options) => client.request({ ...options, method: requestMethod })
    const sseDispatch = client.sse[method]
    client.sse[method] = (options) => sseDispatch(normalizeRequestOptions(options))
  }
}

specDefaultClient.setConfig({ maxRetries: 0, timeoutMs: 0 })
configureRequests(specDefaultClient)

export function createOmnaraClient(options: OmnaraClientOptions = {}): OmnaraClient {
  const client = createClient(
    createConfig({
      throwOnError: true,
    }),
  )
  configureRequests(client)
  client.setConfig({
    baseUrl: options.baseUrl ?? specDefaultClient.getConfig().baseUrl,
    credentials: options.credentials,
    headers: options.headers,
    fetch: options.fetch,
    maxRetries: options.maxRetries,
    timeoutMs: options.timeoutMs,
    onRequestComplete: options.onRequestComplete,
  })
  const { auth } = options
  if (auth) {
    client.interceptors.request.use(async (request) => {
      await auth.authenticate(request)
      return request
    })
  }
  client.interceptors.response.use(async (response) => {
    if (!response.ok) throw await ApiError.fromResponse(response)
    return response
  })
  return client
}
