import type { AuthStrategy } from './auth'
import { ApiError } from './errors'
import { createClient, createConfig, formDataBodySerializer } from './generated/client'
import { client as specDefaultClient } from './generated/client.gen'
import { serializeMultipartBody } from './multipart-body'

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

function normalizeRequestOptions<O extends object>(options: O): O {
  const normalized: O & { bodySerializer?: unknown } = { ...options }
  Reflect.deleteProperty(normalized, 'client')
  if (normalized.bodySerializer === formDataBodySerializer.bodySerializer) {
    normalized.bodySerializer = serializeMultipartBody
  }
  return normalized
}

// Two fixes for generated per-call options, applied before dispatch:
// - The generated client leaks options — including the `client` selector —
//   into the Request init, which throws on Deno and Bun (they reserve the
//   `client` init key). TODO: remove once hey-api/hey-api#4177 is fixed and
//   regenerated.
// - Multipart operations get a serializer that sends object fields as JSON
//   parts; see multipart-body.ts. A caller-supplied bodySerializer is kept.
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
