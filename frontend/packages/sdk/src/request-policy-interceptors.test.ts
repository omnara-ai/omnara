import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { createOmnaraClient } from './client'

const baseUrl = 'https://api.example.test'
const keyedURL = '/orgs/{orgID}/projects/{projectID}/agents'
const idempotentURL = `${keyedURL}/{agentID}/archive`
const path = { orgID: 'org_test', projectID: 'proj_test', agentID: 'agt_test' }
const failure = () => Response.json({ error: 'try later' }, { status: 503 })

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
  vi.spyOn(Math, 'random').mockReturnValue(0)
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

it.each([
  { method: 'GET', url: '/read', rewrittenMethod: 'POST', destination: `${baseUrl}/read` },
  { method: 'POST', url: keyedURL, rewrittenMethod: 'POST', destination: `${baseUrl}/unsafe` },
  {
    method: 'POST',
    url: idempotentURL,
    rewrittenMethod: 'POST',
    destination: 'https://external.example.test/unsafe?private=value',
  },
] as const)('does not replay a request interceptor rewrite: $url', async (rewrite) => {
  const fetch = vi.fn<typeof globalThis.fetch>(() => Promise.resolve(failure()))
  const onRequestComplete = vi.fn()
  const client = createOmnaraClient({ baseUrl, fetch, onRequestComplete })
  client.interceptors.request.use(
    (request) =>
      new Request(rewrite.destination, {
        method: rewrite.rewrittenMethod,
        headers: request.headers,
        signal: request.signal,
      }),
  )
  const result = expect(
    client.request({ method: rewrite.method, url: rewrite.url, path }),
  ).rejects.toMatchObject({ status: 503 })
  await vi.runAllTimersAsync()
  await result
  expect(fetch).toHaveBeenCalledOnce()
  expect(fetch.mock.calls[0]?.[0]).toMatchObject({
    method: rewrite.rewrittenMethod,
    url: rewrite.destination,
  })
  expect(onRequestComplete.mock.calls).toMatchObject([[{ operation: 'unknown', attempts: 1 }]])
  expect(JSON.stringify(onRequestComplete.mock.calls)).not.toContain('private')
})

it.each(['method', 'url', 'baseUrl'] as const)(
  'does not replay a validator rewrite of the original %s',
  async (field) => {
    const fetch = vi.fn<typeof globalThis.fetch>(() => Promise.resolve(failure()))
    const client = createOmnaraClient({ baseUrl, fetch })
    const result = expect(
      client.request({
        method: field === 'method' ? 'GET' : 'POST',
        url: field === 'method' ? '/read' : keyedURL,
        path,
        requestValidator: (options) => {
          // The generated client passes its mutable resolved request options to this callback.
          Object.assign(options ?? {}, {
            [field]: {
              method: 'POST',
              url: '/unsafe',
              baseUrl: 'https://external.example.test',
            }[field],
          })
          return Promise.resolve()
        },
      }),
    ).rejects.toMatchObject({ status: 503 })
    await vi.runAllTimersAsync()
    await result
    expect(fetch).toHaveBeenCalledOnce()
  },
)

it.each(['https://regional.example.test', 'https://regional.example.test/'])(
  'permits an explicit per-request base URL: %s',
  async (requestBaseUrl) => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockImplementationOnce(() => Promise.resolve(failure()))
      .mockResolvedValueOnce(Response.json({ ok: true }))
    const client = createOmnaraClient({ baseUrl, fetch })
    const result = client.post({
      baseUrl: requestBaseUrl,
      url: keyedURL,
      path,
    })
    await vi.runAllTimersAsync()
    await expect(result).resolves.toMatchObject({ data: { ok: true } })
    expect(fetch).toHaveBeenCalledTimes(2)
    for (const [request] of fetch.mock.calls) {
      expect(request).toMatchObject({
        url: `${requestBaseUrl}/orgs/org_test/projects/proj_test/agents`,
      })
    }
  },
)

it('does not inherit a configured base URL over an explicit undefined override', async () => {
  const fetch = vi.fn<typeof globalThis.fetch>(() => Promise.resolve(failure()))
  const client = createOmnaraClient({ baseUrl, fetch })
  const result = expect(
    client.post({
      baseUrl: undefined,
      url: keyedURL,
      path,
      requestValidator: (options) => {
        Object.assign(options ?? {}, { baseUrl })
        return Promise.resolve()
      },
    }),
  ).rejects.toMatchObject({ status: 503 })
  await vi.runAllTimersAsync()
  await result
  expect(fetch).toHaveBeenCalledOnce()
})

it('fails closed when the client request interceptors have been cleared', async () => {
  const fetch = vi.fn<typeof globalThis.fetch>(() => Promise.resolve(failure()))
  const client = createOmnaraClient({ baseUrl, fetch })
  client.interceptors.request.clear()
  const result = expect(client.get({ url: '/read' })).rejects.toMatchObject({ status: 503 })
  await vi.runAllTimersAsync()
  await result
  expect(fetch).toHaveBeenCalledOnce()
})

it('keeps concurrent logical request destinations independent', async () => {
  const attempts = new Map<string, number>()
  const client = createOmnaraClient({
    baseUrl,
    fetch: (input) => {
      const { url } = new Request(input)
      const attempt = (attempts.get(url) ?? 0) + 1
      attempts.set(url, attempt)
      return Promise.resolve(attempt === 1 ? failure() : Response.json({ ok: true }))
    },
  })
  const results = Promise.all([client.get({ url: '/first' }), client.get({ url: '/second' })])
  await vi.runAllTimersAsync()
  await expect(results).resolves.toMatchObject([{ data: { ok: true } }, { data: { ok: true } }])
  expect([...attempts.values()]).toEqual([2, 2])
})
