import * as z from 'zod'

import { ApiError } from './errors'

export const OMNARA_CLI_OAUTH_CLIENT_ID = 'omnara-cli'
export const OAUTH_DEVICE_GRANT_TYPE = 'urn:ietf:params:oauth:grant-type:device_code'

const zAuthorizationServerMetadata = z.object({
  issuer: z.url(),
  device_authorization_endpoint: z.url(),
  token_endpoint: z.url(),
  grant_types_supported: z.array(z.string()),
  token_endpoint_auth_methods_supported: z.array(z.string()),
})

const zDeviceAuthStartResponse = z.object({
  device_code: z.string().min(1),
  user_code: z.string().min(1),
  verification_uri: z.string().min(1),
  verification_uri_complete: z.string().min(1),
  expires_in: z.number().int().positive(),
  interval: z.number().int().positive(),
})

const zDeviceAuthErrorResponse = z.object({
  error: z.string().min(1),
  error_description: z.string().optional(),
})

const zDeviceAuthApprovedResponse = z.object({
  access_token: z.string().min(1),
  token_type: z.string(),
})

export type DeviceAuthFailureCode = 'access_denied' | 'expired_token'

const DEVICE_AUTH_FAILURE_MESSAGES: Record<DeviceAuthFailureCode, string> = {
  access_denied: 'the login request was denied',
  expired_token: 'the login request expired before it was approved',
}

export class DeviceAuthError extends Error {
  readonly code: DeviceAuthFailureCode

  constructor(code: DeviceAuthFailureCode) {
    super(DEVICE_AUTH_FAILURE_MESSAGES[code])
    this.name = 'DeviceAuthError'
    this.code = code
  }
}

export interface DeviceAuthStart {
  deviceCode: string
  userCode: string
  verificationUri: string
  verificationUriComplete: string
  expiresInSeconds: number
  intervalSeconds: number
  tokenEndpoint: string
  clientId: string
}

async function postOAuthForm(
  fetchImpl: typeof fetch,
  endpoint: string,
  body: Record<string, string>,
): Promise<Response> {
  return fetchImpl(endpoint, {
    method: 'POST',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/x-www-form-urlencoded',
    },
    body: new URLSearchParams(body).toString(),
  })
}

export interface StartDeviceAuthOptions {
  issuerUrl: string
  clientId: string
  tokenName?: string
  fetch?: typeof fetch
}

function normalizeIssuer(raw: string): string {
  const issuer = new URL(raw)
  if (
    issuer.username !== '' ||
    issuer.password !== '' ||
    issuer.pathname !== '/' ||
    issuer.search !== '' ||
    issuer.hash !== ''
  ) {
    throw new Error('authorization server issuer must be an origin')
  }
  return issuer.origin
}

export async function startDeviceAuth(options: StartDeviceAuthOptions): Promise<DeviceAuthStart> {
  const fetchImpl = options.fetch ?? fetch
  const expectedIssuer = normalizeIssuer(options.issuerUrl)
  const metadataUrl = new URL('/.well-known/oauth-authorization-server', `${expectedIssuer}/`)
  const metadataResponse = await fetchImpl(metadataUrl, { headers: { Accept: 'application/json' } })
  if (!metadataResponse.ok) throw await ApiError.fromResponse(metadataResponse)
  const metadata = zAuthorizationServerMetadata.parse(await metadataResponse.json())
  if (normalizeIssuer(metadata.issuer) !== expectedIssuer) {
    throw new Error(
      `authorization server issuer mismatch: expected ${expectedIssuer}, received ${metadata.issuer}`,
    )
  }
  if (!metadata.grant_types_supported.includes(OAUTH_DEVICE_GRANT_TYPE)) {
    throw new Error('authorization server does not support the OAuth device grant')
  }
  if (!metadata.token_endpoint_auth_methods_supported.includes('none')) {
    throw new Error('authorization server does not support public OAuth clients')
  }
  const body: Record<string, string> = { client_id: options.clientId }
  if (options.tokenName !== undefined) body.token_name = options.tokenName
  const response = await postOAuthForm(fetchImpl, metadata.device_authorization_endpoint, body)
  if (!response.ok) throw await ApiError.fromResponse(response)
  const data = zDeviceAuthStartResponse.parse(await response.json())
  return {
    deviceCode: data.device_code,
    userCode: data.user_code,
    verificationUri: new URL(data.verification_uri, metadata.issuer).toString(),
    verificationUriComplete: new URL(data.verification_uri_complete, metadata.issuer).toString(),
    expiresInSeconds: data.expires_in,
    intervalSeconds: data.interval,
    tokenEndpoint: metadata.token_endpoint,
    clientId: options.clientId,
  }
}

function defaultSleep(seconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, seconds * 1000))
}

export interface PollDeviceAuthTokenOptions {
  tokenEndpoint: string
  clientId: string
  deviceCode: string
  intervalSeconds: number
  fetch?: typeof fetch
  sleep?: (seconds: number) => Promise<void>
}

export async function pollDeviceAuthToken(options: PollDeviceAuthTokenOptions): Promise<string> {
  const fetchImpl = options.fetch ?? fetch
  const sleep = options.sleep ?? defaultSleep
  let interval = options.intervalSeconds
  while (true) {
    await sleep(interval)
    const response = await postOAuthForm(fetchImpl, options.tokenEndpoint, {
      grant_type: OAUTH_DEVICE_GRANT_TYPE,
      device_code: options.deviceCode,
      client_id: options.clientId,
    })
    if (response.status === 200) {
      return zDeviceAuthApprovedResponse.parse(await response.json()).access_token
    }
    if (response.status === 400) {
      const failure = zDeviceAuthErrorResponse.safeParse(
        await response
          .clone()
          .json()
          .catch((): unknown => undefined),
      )
      if (failure.success) {
        if (failure.data.error === 'authorization_pending') continue
        if (failure.data.error === 'slow_down') {
          interval += 5
          continue
        }
        if (failure.data.error === 'access_denied' || failure.data.error === 'expired_token') {
          throw new DeviceAuthError(failure.data.error)
        }
      }
    }
    throw await ApiError.fromResponse(response)
  }
}
