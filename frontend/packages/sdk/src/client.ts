import type { AuthStrategy } from './auth'
import { ApiError } from './errors'
import { createClient, createConfig } from './generated/client'

export type OmnaraClient = ReturnType<typeof createClient>

export interface OmnaraClientOptions {
  baseUrl?: string
  credentials?: RequestCredentials
  auth?: AuthStrategy
  headers?: Record<string, string>
}

type Dispatch = (options?: Record<string, unknown>) => unknown

// The generated client leaks per-call options — including the `client`
// selector — into the Request init, which throws on Deno and Bun (they
// reserve the `client` init key). Drop the key before dispatch.
// TODO: remove once hey-api/hey-api#4177 is fixed and regenerated.
function stripClientSelector(client: OmnaraClient): void {
  const strip =
    (dispatch: Dispatch): Dispatch =>
    (options) => {
      if (options == null || !('client' in options)) return dispatch(options)
      const rest = { ...options }
      delete rest.client
      return dispatch(rest)
    }
  const methods = [
    'connect',
    'delete',
    'get',
    'head',
    'options',
    'patch',
    'post',
    'put',
    'request',
    'trace',
  ] as const
  const wrap = (target: object) => {
    const dispatches = target as Partial<Record<(typeof methods)[number], Dispatch>>
    for (const method of methods) dispatches[method] &&= strip(dispatches[method])
  }
  wrap(client)
  wrap(client.sse)
}

export function createOmnaraClient(options: OmnaraClientOptions = {}): OmnaraClient {
  const client = createClient(
    createConfig({
      throwOnError: true,
    }),
  )
  client.setConfig({
    ...(options.baseUrl === undefined ? {} : { baseUrl: options.baseUrl }),
    credentials: options.credentials,
    headers: options.headers,
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
