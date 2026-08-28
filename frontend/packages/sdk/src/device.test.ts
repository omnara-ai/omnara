import { describe, expect, it } from 'vitest'

import {
  DeviceAuthError,
  OAUTH_DEVICE_GRANT_TYPE,
  pollDeviceAuthToken,
  startDeviceAuth,
} from './device'
import { ApiError } from './errors'

interface RecordedRequest {
  url: string
  method: string | undefined
  contentType: string | undefined
  body: Record<string, string> | undefined
}

function fetchQueue(responses: Response[]): {
  fetch: typeof fetch
  requests: RecordedRequest[]
} {
  const requests: RecordedRequest[] = []
  const queued = [...responses]
  const fetchImpl: typeof fetch = (input, init) => {
    const headers = new Headers(init?.headers)
    const contentType = headers.get('Content-Type') ?? undefined
    requests.push({
      url: typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url,
      method: init?.method,
      contentType,
      body:
        typeof init?.body === 'string' && contentType === 'application/x-www-form-urlencoded'
          ? Object.fromEntries(new URLSearchParams(init.body))
          : undefined,
    })
    const next = queued.shift()
    if (next === undefined) throw new Error('fetch queue exhausted')
    return Promise.resolve(next)
  }
  return { fetch: fetchImpl, requests }
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function recordedSleep(): { sleep: (seconds: number) => Promise<void>; sleeps: number[] } {
  const sleeps: number[] = []
  return {
    sleep: (seconds) => {
      sleeps.push(seconds)
      return Promise.resolve()
    },
    sleeps,
  }
}

const CLIENT_ID = 'omnara-cli'
const METADATA = {
  issuer: 'https://app.example.com',
  device_authorization_endpoint: 'https://app.example.com/api/auth/device/code',
  token_endpoint: 'https://app.example.com/api/auth/device/token',
  grant_types_supported: [OAUTH_DEVICE_GRANT_TYPE],
  token_endpoint_auth_methods_supported: ['none'],
}
const START_BODY = {
  device_code: 'device-code-1',
  user_code: 'ABCDE-FGHIJ',
  verification_uri: 'https://app.example.com/device',
  verification_uri_complete: 'https://app.example.com/device?user_code=ABCDE-FGHIJ',
  expires_in: 900,
  interval: 5,
}

describe('startDeviceAuth', () => {
  it('discovers the authorization server and starts an RFC 8628 device grant', async () => {
    const { fetch, requests } = fetchQueue([
      jsonResponse(200, METADATA),
      jsonResponse(200, START_BODY),
    ])
    const start = await startDeviceAuth({
      issuerUrl: 'https://APP.EXAMPLE.com:443/',
      clientId: CLIENT_ID,
      tokenName: 'CLI on laptop',
      fetch,
    })
    expect(requests).toEqual([
      {
        url: 'https://app.example.com/.well-known/oauth-authorization-server',
        method: undefined,
        contentType: undefined,
        body: undefined,
      },
      {
        url: 'https://app.example.com/api/auth/device/code',
        method: 'POST',
        contentType: 'application/x-www-form-urlencoded',
        body: { client_id: CLIENT_ID, token_name: 'CLI on laptop' },
      },
    ])
    expect(start).toEqual({
      deviceCode: 'device-code-1',
      userCode: 'ABCDE-FGHIJ',
      verificationUri: 'https://app.example.com/device',
      verificationUriComplete: 'https://app.example.com/device?user_code=ABCDE-FGHIJ',
      expiresInSeconds: 900,
      intervalSeconds: 5,
      tokenEndpoint: 'https://app.example.com/api/auth/device/token',
      clientId: CLIENT_ID,
    })
  })

  it('rejects authorization-server issuer substitution', async () => {
    const { fetch } = fetchQueue([
      jsonResponse(200, { ...METADATA, issuer: 'https://attacker.example.com' }),
    ])
    await expect(
      startDeviceAuth({
        issuerUrl: 'https://app.example.com',
        clientId: CLIENT_ID,
        tokenName: 'CLI',
        fetch,
      }),
    ).rejects.toThrow('authorization server issuer mismatch')
  })

  it.each([
    [
      'the device grant',
      { ...METADATA, grant_types_supported: [] },
      'authorization server does not support the OAuth device grant',
    ],
    [
      'public clients',
      { ...METADATA, token_endpoint_auth_methods_supported: ['client_secret_basic'] },
      'authorization server does not support public OAuth clients',
    ],
  ])('rejects metadata without %s', async (_capability, metadata, message) => {
    const { fetch } = fetchQueue([jsonResponse(200, metadata)])
    await expect(
      startDeviceAuth({
        issuerUrl: 'https://app.example.com',
        clientId: CLIENT_ID,
        tokenName: 'CLI',
        fetch,
      }),
    ).rejects.toThrow(message)
  })

  it('throws ApiError when the flow cannot start', async () => {
    const { fetch } = fetchQueue([
      jsonResponse(200, METADATA),
      jsonResponse(429, {
        error: 'temporarily_unavailable',
        error_description: 'rate limit exceeded',
      }),
    ])
    await expect(
      startDeviceAuth({
        issuerUrl: 'https://app.example.com',
        clientId: CLIENT_ID,
        tokenName: 'CLI',
        fetch,
      }),
    ).rejects.toThrow('rate limit exceeded')
  })

  it('resolves relative verification URLs against the discovered issuer', async () => {
    const localMetadata = {
      ...METADATA,
      issuer: 'http://localhost:8080',
      device_authorization_endpoint: 'http://localhost:8080/api/auth/device/code',
      token_endpoint: 'http://localhost:8080/api/auth/device/token',
    }
    const { fetch } = fetchQueue([
      jsonResponse(200, localMetadata),
      jsonResponse(200, {
        ...START_BODY,
        verification_uri: '/device',
        verification_uri_complete: '/device?user_code=ABCDE-FGHIJ',
      }),
    ])
    const start = await startDeviceAuth({
      issuerUrl: 'http://localhost:8080',
      clientId: CLIENT_ID,
      tokenName: 'CLI',
      fetch,
    })
    expect(start.verificationUri).toBe('http://localhost:8080/device')
    expect(start.verificationUriComplete).toBe('http://localhost:8080/device?user_code=ABCDE-FGHIJ')
  })
})

describe('pollDeviceAuthToken', () => {
  it('polls the OAuth token endpoint and returns the token on approval', async () => {
    const { fetch, requests } = fetchQueue([
      jsonResponse(400, { error: 'authorization_pending' }),
      jsonResponse(200, { access_token: 'omnara_pat_v1_abc', token_type: 'Bearer' }),
    ])
    const { sleep, sleeps } = recordedSleep()
    const token = await pollDeviceAuthToken({
      tokenEndpoint: METADATA.token_endpoint,
      clientId: CLIENT_ID,
      deviceCode: 'device-code-1',
      intervalSeconds: 5,
      fetch,
      sleep,
    })
    expect(token).toBe('omnara_pat_v1_abc')
    expect(sleeps).toEqual([5, 5])
    expect(requests.map((request) => request.url)).toEqual([
      METADATA.token_endpoint,
      METADATA.token_endpoint,
    ])
    expect(requests[0]?.body).toEqual({
      grant_type: OAUTH_DEVICE_GRANT_TYPE,
      device_code: 'device-code-1',
      client_id: CLIENT_ID,
    })
  })

  it('adds five seconds to the polling interval after slow_down', async () => {
    const { fetch } = fetchQueue([
      jsonResponse(400, { error: 'slow_down' }),
      jsonResponse(200, { access_token: 'omnara_pat_v1_abc', token_type: 'Bearer' }),
    ])
    const { sleep, sleeps } = recordedSleep()
    await pollDeviceAuthToken({
      tokenEndpoint: METADATA.token_endpoint,
      clientId: CLIENT_ID,
      deviceCode: 'device-code-1',
      intervalSeconds: 5,
      fetch,
      sleep,
    })
    expect(sleeps).toEqual([5, 10])
  })

  it('throws DeviceAuthError when the request is denied', async () => {
    const { fetch } = fetchQueue([jsonResponse(400, { error: 'access_denied' })])
    const { sleep } = recordedSleep()
    const poll = pollDeviceAuthToken({
      tokenEndpoint: METADATA.token_endpoint,
      clientId: CLIENT_ID,
      deviceCode: 'device-code-1',
      intervalSeconds: 5,
      fetch,
      sleep,
    })
    await expect(poll).rejects.toMatchObject({
      name: 'DeviceAuthError',
      code: 'access_denied',
    })
  })

  it('throws DeviceAuthError when the request expires', async () => {
    const { fetch } = fetchQueue([jsonResponse(400, { error: 'expired_token' })])
    const { sleep } = recordedSleep()
    await expect(
      pollDeviceAuthToken({
        tokenEndpoint: METADATA.token_endpoint,
        clientId: CLIENT_ID,
        deviceCode: 'device-code-1',
        intervalSeconds: 5,
        fetch,
        sleep,
      }),
    ).rejects.toBeInstanceOf(DeviceAuthError)
  })

  it('throws ApiError on an unrecognized OAuth failure', async () => {
    const { fetch } = fetchQueue([jsonResponse(400, { error: 'invalid_grant' })])
    const { sleep } = recordedSleep()
    await expect(
      pollDeviceAuthToken({
        tokenEndpoint: METADATA.token_endpoint,
        clientId: CLIENT_ID,
        deviceCode: 'device-code-1',
        intervalSeconds: 5,
        fetch,
        sleep,
      }),
    ).rejects.toBeInstanceOf(ApiError)
  })
})
