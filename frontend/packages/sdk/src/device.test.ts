import { describe, expect, it } from 'vitest'

import { DeviceAuthError, pollDeviceAuthToken, startDeviceAuth } from './device'
import { ApiError } from './errors'

interface RecordedRequest {
  url: string
  method: string | undefined
  contentType: string | undefined
  body: unknown
}

function fetchQueue(responses: Response[]): {
  fetch: typeof fetch
  requests: RecordedRequest[]
} {
  const requests: RecordedRequest[] = []
  const queued = [...responses]
  const fetchImpl: typeof fetch = (input, init) => {
    const headers = new Headers(init?.headers)
    requests.push({
      url: typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url,
      method: init?.method,
      contentType: headers.get('Content-Type') ?? undefined,
      body: typeof init?.body === 'string' ? JSON.parse(init.body) : init?.body,
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

const START_BODY = {
  device_code: 'device-code-1',
  user_code: 'ABCDE-FGHIJ',
  verification_uri: 'https://app.example.com/device',
  verification_uri_complete: 'https://app.example.com/device?user_code=ABCDE-FGHIJ',
  expires_in: 900,
  interval: 5,
}

describe('startDeviceAuth', () => {
  it('posts the client and token names and maps the response', async () => {
    const { fetch, requests } = fetchQueue([jsonResponse(200, START_BODY)])
    const start = await startDeviceAuth({
      baseUrl: 'https://app.example.com/',
      clientName: 'Omnara CLI',
      tokenName: 'CLI on laptop',
      fetch,
    })
    expect(requests).toEqual([
      {
        url: 'https://app.example.com/api/auth/device/code',
        method: 'POST',
        contentType: 'application/json',
        body: { client_name: 'Omnara CLI', token_name: 'CLI on laptop' },
      },
    ])
    expect(start).toEqual({
      deviceCode: 'device-code-1',
      userCode: 'ABCDE-FGHIJ',
      verificationUri: 'https://app.example.com/device',
      verificationUriComplete: 'https://app.example.com/device?user_code=ABCDE-FGHIJ',
      expiresInSeconds: 900,
      intervalSeconds: 5,
    })
  })

  it('throws ApiError when the flow cannot start', async () => {
    const { fetch } = fetchQueue([jsonResponse(429, { error: 'rate limited' })])
    await expect(
      startDeviceAuth({
        baseUrl: 'https://app.example.com',
        clientName: 'x',
        tokenName: 'y',
        fetch,
      }),
    ).rejects.toThrow(ApiError)
  })

  it('resolves relative verification URLs against the configured base URL', async () => {
    const { fetch } = fetchQueue([
      jsonResponse(200, {
        ...START_BODY,
        verification_uri: '/device',
        verification_uri_complete: '/device?user_code=ABCDE-FGHIJ',
      }),
    ])
    const start = await startDeviceAuth({
      baseUrl: 'http://localhost:8080',
      clientName: 'Omnara CLI',
      tokenName: 'CLI on laptop',
      fetch,
    })
    expect(start.verificationUri).toBe('http://localhost:8080/device')
    expect(start.verificationUriComplete).toBe('http://localhost:8080/device?user_code=ABCDE-FGHIJ')
  })
})

describe('pollDeviceAuthToken', () => {
  it('sleeps before each poll and returns the token on approval', async () => {
    const { fetch, requests } = fetchQueue([
      jsonResponse(202, { error: 'authorization_pending', interval: 5 }),
      jsonResponse(200, { access_token: 'omnara_pat_v1_abc', token_type: 'Bearer' }),
    ])
    const { sleep, sleeps } = recordedSleep()
    const token = await pollDeviceAuthToken({
      baseUrl: 'https://app.example.com',
      deviceCode: 'device-code-1',
      intervalSeconds: 5,
      fetch,
      sleep,
    })
    expect(token).toBe('omnara_pat_v1_abc')
    expect(sleeps).toEqual([5, 5])
    expect(requests.map((request) => request.url)).toEqual([
      'https://app.example.com/api/auth/device/token',
      'https://app.example.com/api/auth/device/token',
    ])
    expect(requests[0]?.body).toEqual({ device_code: 'device-code-1' })
  })

  it('backs off to the interval returned by slow_down', async () => {
    const { fetch } = fetchQueue([
      jsonResponse(202, { error: 'slow_down', interval: 10 }),
      jsonResponse(200, { access_token: 'omnara_pat_v1_abc', token_type: 'Bearer' }),
    ])
    const { sleep, sleeps } = recordedSleep()
    await pollDeviceAuthToken({
      baseUrl: 'https://app.example.com',
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
      baseUrl: 'https://app.example.com',
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
        baseUrl: 'https://app.example.com',
        deviceCode: 'device-code-1',
        intervalSeconds: 5,
        fetch,
        sleep,
      }),
    ).rejects.toBeInstanceOf(DeviceAuthError)
  })

  it('throws ApiError on an unrecognized failure', async () => {
    const { fetch } = fetchQueue([jsonResponse(400, { error: 'validation failed' })])
    const { sleep } = recordedSleep()
    await expect(
      pollDeviceAuthToken({
        baseUrl: 'https://app.example.com',
        deviceCode: 'device-code-1',
        intervalSeconds: 5,
        fetch,
        sleep,
      }),
    ).rejects.toBeInstanceOf(ApiError)
  })
})
