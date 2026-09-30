import * as z from 'zod'

import type { AuthStrategy } from './auth'
import { ApiError } from './errors'
import { createClient, createConfig, formDataBodySerializer } from './generated/client'
import { client as specDefaultClient } from './generated/client.gen'
import type { BodySerializer } from './generated/core/bodySerializer.gen'

export type OmnaraClient = ReturnType<typeof createClient>

export interface OmnaraClientOptions {
  baseUrl?: string
  credentials?: RequestCredentials
  auth?: AuthStrategy
  headers?: Record<string, string>
  fetch?: typeof fetch
}

type HttpMethod =
  | 'connect'
  | 'delete'
  | 'get'
  | 'head'
  | 'options'
  | 'patch'
  | 'post'
  | 'put'
  | 'trace'

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

// Drops the leaked `client` init key, which throws on Deno and Bun (TODO:
// remove once hey-api/hey-api#4177 is fixed and regenerated), and sends object
// multipart fields as JSON parts. The default client is patched too because
// generated operations fall back to it when no `client` is passed.
function patchGeneratedRequests(client: OmnaraClient): void {
  const { request } = client
  client.request = (options) => request(normalizeRequestOptions(options))
  for (const method of httpMethods) {
    const dispatch = client[method]
    client[method] = (options) => dispatch(normalizeRequestOptions(options))
    const sseDispatch = client.sse[method]
    client.sse[method] = (options) => sseDispatch(normalizeRequestOptions(options))
  }
}

patchGeneratedRequests(specDefaultClient)

export function createOmnaraClient(options: OmnaraClientOptions = {}): OmnaraClient {
  const client = createClient(
    createConfig({
      throwOnError: true,
    }),
  )
  client.setConfig({
    baseUrl: options.baseUrl ?? specDefaultClient.getConfig().baseUrl,
    credentials: options.credentials,
    headers: options.headers,
    fetch: options.fetch,
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
  patchGeneratedRequests(client)
  return client
}
