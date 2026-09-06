import type { AuthStrategy } from './auth'
import { ApiError } from './errors'
import { createClient, createConfig } from './generated/client'
import { client as specDefaultClient } from './generated/client.gen'

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

function withoutClientSelector<O extends object>(options: O): O {
  const stripped = { ...options }
  Reflect.deleteProperty(stripped, 'client')
  return stripped
}

// The generated client leaks per-call options — including the `client`
// selector — into the Request init, which throws on Deno and Bun (they
// reserve the `client` init key). Drop the key before dispatch.
// TODO: remove once hey-api/hey-api#4177 is fixed and regenerated.
function stripClientSelector(client: OmnaraClient): void {
  const { request } = client
  client.request = (options) => request(withoutClientSelector(options))
  for (const method of httpMethods) {
    const dispatch = client[method]
    client[method] = (options) => dispatch(withoutClientSelector(options))
    const sseDispatch = client.sse[method]
    client.sse[method] = (options) => sseDispatch(withoutClientSelector(options))
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
  stripClientSelector(client)
  return client
}
