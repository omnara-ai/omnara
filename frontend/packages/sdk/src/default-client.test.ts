import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { client } from './generated/client.gen'
import { sdk } from './index'

const originalConfig = client.getConfig()
const path = { orgID: 'org_test', projectID: 'proj_test' }
const error = { error: 'try later', code: 'service_unavailable' }
const failure = () => Response.json(error, { status: 503 })

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
  vi.spyOn(Math, 'random').mockReturnValue(0.5)
})
afterEach(() => {
  client.setConfig(originalConfig)
  vi.restoreAllMocks()
  vi.useRealTimers()
})

it('defaults to one attempt and preserves raw generated error modes', async () => {
  expect(client.getConfig()).toMatchObject({ maxRetries: 0, timeoutMs: 0 })
  const fetch = vi.fn<typeof globalThis.fetch>(() => Promise.resolve(failure()))
  await expect(sdk.listAgents({ path, fetch })).rejects.toEqual(error)
  expect(fetch).toHaveBeenCalledOnce()
  await expect(sdk.listAgents({ path, fetch, throwOnError: false })).resolves.toMatchObject({
    error,
    response: { status: 503 },
  })
  expect(fetch).toHaveBeenCalledTimes(2)
})

it('rejects shared default-client idempotency keys while permitting request keys', async () => {
  expect(() => client.setConfig({ headers: [['Idempotency-Key', 'shared-key']] })).toThrow(
    /set it on each request instead/,
  )
  const fetch = vi.fn<typeof globalThis.fetch>((input) => {
    expect(new Request(input).headers.get('Idempotency-Key')).toBe('request-key')
    return Promise.resolve(Response.json({ ok: true }))
  })
  await client.post({
    url: '/orgs/{orgID}/projects/{projectID}/agents',
    path,
    fetch,
    headers: { 'idempotency-key': 'request-key' },
  })
  expect(fetch).toHaveBeenCalledOnce()
})

it.each(['request', 'client'] as const)('honors explicit %s retries', async (scope) => {
  if (scope === 'client') client.setConfig({ maxRetries: 1 })
  const fetch = vi.fn<typeof globalThis.fetch>(() => Promise.resolve(failure()))
  const result = sdk.listAgents({
    path,
    fetch,
    maxRetries: scope === 'request' ? 1 : undefined,
    throwOnError: false,
  })
  await vi.runAllTimersAsync()
  await expect(result).resolves.toMatchObject({ error, response: { status: 503 } })
  expect(fetch).toHaveBeenCalledTimes(2)
})

it('does not replay an intercepted destination on the shared client', async () => {
  const fetch = vi.fn<typeof globalThis.fetch>(() => Promise.resolve(failure()))
  const interceptor = client.interceptors.request.use(
    (request) =>
      new Request('https://external.example.test/write', {
        method: 'POST',
        signal: request.signal,
      }),
  )
  try {
    const result = sdk.listAgents({ path, fetch, maxRetries: 2, throwOnError: false })
    await vi.runAllTimersAsync()
    await expect(result).resolves.toMatchObject({ error, response: { status: 503 } })
    expect(fetch).toHaveBeenCalledOnce()
  } finally {
    client.interceptors.request.eject(interceptor)
  }
})

it.each(['request', 'client'] as const)(
  'honors explicit %s deadlines in both error modes',
  async (scope) => {
    if (scope === 'client') client.setConfig({ timeoutMs: 10 })
    const options = {
      path,
      fetch: vi.fn<typeof globalThis.fetch>(() => new Promise<Response>(() => undefined)),
      timeoutMs: scope === 'request' ? 10 : undefined,
    }
    const fields = sdk.listAgents({ ...options, throwOnError: false })
    await vi.advanceTimersByTimeAsync(10)
    const result = await fields
    expect(result.error).toMatchObject({ name: 'TimeoutError' })
    expect(result.request).toBeInstanceOf(Request)
    const thrown = expect(sdk.listAgents(options)).rejects.toMatchObject({ name: 'TimeoutError' })
    await vi.advanceTimersByTimeAsync(10)
    await thrown
    expect(options.fetch).toHaveBeenCalledTimes(2)
  },
)
