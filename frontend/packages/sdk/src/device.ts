import * as z from 'zod'

import { ApiError } from './errors'

const zDeviceAuthStartResponse = z.object({
  device_code: z.string().min(1),
  user_code: z.string().min(1),
  verification_uri: z.string().min(1),
  verification_uri_complete: z.string().min(1),
  expires_in: z.number().int().positive(),
  interval: z.number().int().positive(),
})

const zDeviceAuthWaitingResponse = z.object({
  interval: z.number().int().positive(),
})

const zDeviceAuthFailureResponse = z.object({
  error: z.enum(['access_denied', 'expired_token']),
})

const zDeviceAuthApprovedResponse = z.object({
  access_token: z.string().min(1),
  token_type: z.string(),
})

export type DeviceAuthFailureCode = z.infer<typeof zDeviceAuthFailureResponse>['error']

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
}

async function postDeviceAuthJSON(
  fetchImpl: typeof fetch,
  baseUrl: string,
  path: string,
  body: unknown,
): Promise<Response> {
  return fetchImpl(`${baseUrl.replace(/\/+$/, '')}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export interface StartDeviceAuthOptions {
  baseUrl: string
  clientName: string
  tokenName: string
  fetch?: typeof fetch
}

export async function startDeviceAuth(options: StartDeviceAuthOptions): Promise<DeviceAuthStart> {
  const response = await postDeviceAuthJSON(
    options.fetch ?? fetch,
    options.baseUrl,
    '/api/auth/device/code',
    { client_name: options.clientName, token_name: options.tokenName },
  )
  if (!response.ok) throw await ApiError.fromResponse(response)
  const data = zDeviceAuthStartResponse.parse(await response.json())
  return {
    deviceCode: data.device_code,
    userCode: data.user_code,
    verificationUri: new URL(data.verification_uri, options.baseUrl).toString(),
    verificationUriComplete: new URL(data.verification_uri_complete, options.baseUrl).toString(),
    expiresInSeconds: data.expires_in,
    intervalSeconds: data.interval,
  }
}

function defaultSleep(seconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, seconds * 1000))
}

export interface PollDeviceAuthTokenOptions {
  baseUrl: string
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
    const response = await postDeviceAuthJSON(
      fetchImpl,
      options.baseUrl,
      '/api/auth/device/token',
      {
        device_code: options.deviceCode,
      },
    )
    if (response.status === 200) {
      return zDeviceAuthApprovedResponse.parse(await response.json()).access_token
    }
    if (response.status === 202) {
      interval = zDeviceAuthWaitingResponse.parse(await response.json()).interval
      continue
    }
    if (response.status === 400) {
      const failure = zDeviceAuthFailureResponse.safeParse(
        await response
          .clone()
          .json()
          .catch((): unknown => undefined),
      )
      if (failure.success) throw new DeviceAuthError(failure.data.error)
    }
    throw await ApiError.fromResponse(response)
  }
}
