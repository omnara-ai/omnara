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

export function createOmnaraClient(options: OmnaraClientOptions = {}): OmnaraClient {
  const client = createClient(
    createConfig({
      throwOnError: true,
    }),
  )
  client.setConfig({
    baseUrl: options.baseUrl ?? '',
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
  return client
}
