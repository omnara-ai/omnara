import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createOmnaraClient } from './client'
import { ApiError } from './errors'
import { writeMemoryFile } from './generated/sdk.gen'

const baseUrl = 'https://api.example.test/v1'
const agentUrl = '/orgs/{orgID}/projects/{projectID}/agents'
const path = { orgID: 'org_test', projectID: 'proj_test' }
const failure = (status = 503, retryAfter?: string) =>
  Response.json(
    { error: 'try later', code: 'service_unavailable' },
    {
      status,
      headers: retryAfter ? { 'Retry-After': retryAfter } : undefined,
    },
  )

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
  vi.spyOn(Math, 'random').mockReturnValue(0.5)
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('SDK transport retries', () => {
  it.each([408, 429, 500, 502, 503, 504])('retries HTTP %i twice by default', async (status) => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockImplementation(() => Promise.resolve(failure(status)))
    const client = createOmnaraClient({ baseUrl, fetch })
    const done = expect(client.get({ url: '/agents' })).rejects.toMatchObject({ status })
    await vi.runAllTimersAsync()
    await done
    expect(fetch).toHaveBeenCalledTimes(3)
    expect(client.getConfig().fetch).toBe(fetch)
  })

  it.each([400, 401, 403, 404, 409, 413, 422, 501])('does not retry HTTP %i', async (status) => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockImplementation(() => Promise.resolve(failure(status)))
    const client = createOmnaraClient({ baseUrl, fetch })
    await expect(client.get({ url: '/agents' })).rejects.toMatchObject({ status })
    expect(fetch).toHaveBeenCalledOnce()
  })

  it('replays the same authenticated bytes and key after a lost response', async () => {
    const sent: { body: string; key: string | null; auth: string | null }[] = []
    const fetch = vi.fn<typeof globalThis.fetch>(async (input) => {
      const request = new Request(input)
      sent.push({
        body: await request.text(),
        key: request.headers.get('Idempotency-Key'),
        auth: request.headers.get('Authorization'),
      })
      if (sent.length === 1) throw new TypeError('connection reset after commit')
      return Response.json({ id: 'agent_once' })
    })
    const authenticate = vi.fn((request: Request) => {
      request.headers.set('Authorization', 'Bearer original')
    })
    const bodySerializer = vi.fn(JSON.stringify)
    const querySerializer = vi.fn(() => 'include=summary')
    const client = createOmnaraClient({ baseUrl, fetch, auth: { authenticate } })
    const intercept = vi.fn((request: Request) => {
      const cloned = request.clone()
      cloned.headers.set('X-Caller', 'intercepted')
      return cloned
    })
    client.interceptors.request.use(intercept)
    const validate = vi.fn(() => Promise.resolve())
    const done = client.post({
      url: agentUrl,
      path,
      body: { name: 'Ada' },
      bodySerializer,
      query: { include: 'summary' },
      querySerializer,
      responseValidator: validate,
    })
    await vi.runAllTimersAsync()
    expect(await done).toMatchObject({ data: { id: 'agent_once' } })
    expect(sent).toHaveLength(2)
    expect(sent[0]?.key).toMatch(/^[0-9a-f-]{36}$/)
    expect(sent[1]).toEqual(sent[0])
    expect(authenticate).toHaveBeenCalledOnce()
    expect(bodySerializer).toHaveBeenCalledOnce()
    expect(querySerializer).toHaveBeenCalledOnce()
    expect(intercept).toHaveBeenCalledOnce()
    expect(validate).toHaveBeenCalledOnce()
    await client.post({ url: agentUrl, path, body: { name: 'Ada' }, maxRetries: 0 })
    expect(sent[2]?.key).not.toBe(sent[0]?.key)
  })

  it.each(
    (
      [
        { 'iDeMpOtEnCy-KeY': 'caller-key', 'X-Client': 'caller value' },
        new Headers({ 'Idempotency-Key': 'caller-key', 'X-Client': 'caller value' }),
        [
          ['idempotency-key', 'caller-key'],
          ['x-client', 'caller value'],
        ],
      ] satisfies HeadersInit[]
    ).map((headers) => ({ headers })),
  )('preserves caller keys in every header representation', async ({ headers }) => {
    const sent: Headers[] = []
    const client = createOmnaraClient({
      baseUrl,
      headers: { 'X-Client': 'config value' },
      fetch: (input) => {
        sent.push(new Request(input).headers)
        return Promise.resolve(Response.json({}))
      },
    })
    await client.post({ url: agentUrl, path, headers, maxRetries: 0 })
    await client.post({ url: agentUrl, path, maxRetries: 0 })
    expect(sent.map((headers) => headers.get('X-Client'))).toEqual(['caller value', 'config value'])
    expect(sent[0]?.get('Idempotency-Key')).toBe('caller-key')
    expect(sent[1]?.get('Idempotency-Key')).toMatch(/^[0-9a-f-]{36}$/)
  })

  it.each(
    (
      [
        { 'iDeMpOtEnCy-KeY': 'shared-key' },
        new Headers({ 'Idempotency-Key': 'shared-key' }),
        [['idempotency-key', 'shared-key']],
        { 'Idempotency-Key': '' },
      ] satisfies HeadersInit[]
    ).map((headers) => ({ headers })),
  )('rejects client-wide keys at creation and setConfig', ({ headers }) => {
    expect(() => createOmnaraClient({ headers })).toThrow(/set it on each request instead/)
    const client = createOmnaraClient({ headers: { 'X-Client': 'original' } })
    expect(() => client.setConfig({ headers })).toThrow(/set it on each request instead/)
  })

  it.each(['POST', 'PUT', 'PATCH', 'DELETE'] as const)(
    'does not infer replay safety for arbitrary %s requests or headers',
    async (method) => {
      const fetch = vi
        .fn<typeof globalThis.fetch>()
        .mockImplementation(() => Promise.resolve(failure()))
      const client = createOmnaraClient({ baseUrl, fetch })
      await expect(
        client.request({
          method,
          url: '/unclassified-write',
          headers: { 'Idempotency-Key': 'not-a-server-guarantee' },
        }),
      ).rejects.toBeInstanceOf(ApiError)
      expect(fetch).toHaveBeenCalledOnce()
    },
  )

  it('preserves per-call removal of client headers while generating a fresh key', async () => {
    const sent: Headers[] = []
    const client = createOmnaraClient({
      baseUrl,
      headers: { 'X-Optional': 'config value' },
      fetch: (input) => {
        sent.push(new Request(input).headers)
        return Promise.resolve(Response.json({}))
      },
    })
    await client.post({
      url: agentUrl,
      path,
      headers: { 'X-Optional': null, 'idempotency-key': null },
    })
    expect(sent[0]?.has('X-Optional')).toBe(false)
    expect(sent[0]?.get('Idempotency-Key')).toMatch(/^[0-9a-f-]{36}$/)
  })

  it.each([
    ['POST', '/orgs/{orgID}/projects/{projectID}/agents/{agentID}/archive'],
    ['POST', '/orgs/{orgID}/projects/{projectID}/agents/{agentID}/tool-calls/{toolCallID}/result'],
  ] as const)('retries the declared idempotent operation %s %s', async (method, url) => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockImplementationOnce(() => Promise.resolve(failure()))
      .mockImplementationOnce(() => Promise.resolve(failure(409)))
    const client = createOmnaraClient({ baseUrl, fetch })
    const done = expect(
      client.request({ method, url, path: { ...path, agentID: 'agent', toolCallID: 'call' } }),
    ).rejects.toMatchObject({ status: 409 })
    await vi.runAllTimersAsync()
    await done
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('replays binary memory uploads with exactly the original bytes', async () => {
    vi.useRealTimers()
    vi.mocked(Math.random).mockReturnValue(0)
    const sent: string[] = []
    const client = createOmnaraClient({
      baseUrl,
      fetch: async (input) => {
        sent.push(await new Request(input).text())
        return failure()
      },
    })
    const done = expect(
      writeMemoryFile({
        client,
        path: { ...path, memoryStoreID: 'mem_test' },
        query: { path: 'notes.txt' },
        body: new Blob(['memory\u0000content']),
      }),
    ).rejects.toMatchObject({ status: 503 })
    await done
    expect(sent).toEqual(Array(3).fill('memory\u0000content'))
  })

  it('serializes multipart boundaries once, preserving files across attempts', async () => {
    vi.useRealTimers()
    vi.mocked(Math.random).mockReturnValue(0)
    const sent: string[] = []
    const form = new FormData()
    form.append('file', new Blob(['attachment']), 'note.txt')
    const client = createOmnaraClient({
      baseUrl,
      fetch: async (input) => {
        sent.push(await new Request(input).text())
        return sent.length === 1 ? failure() : Response.json({})
      },
    })
    const done = client.post({ url: agentUrl, path, body: form, bodySerializer: null })
    await done
    expect(sent).toHaveLength(2)
    expect(sent[0]).toContain('attachment')
    expect(sent[1]).toBe(sent[0])
  })

  it.each(['body', 'serializer'] as const)(
    'does not replay a one-shot stream from the %s',
    async (source) => {
      const stream = new ReadableStream({
        start(controller) {
          controller.enqueue(new TextEncoder().encode('data'))
          controller.close()
        },
      })
      const fetch = vi
        .fn<typeof globalThis.fetch>()
        .mockImplementation(() => Promise.resolve(failure()))
      const client = createOmnaraClient({ baseUrl, fetch })
      const options = {
        url: agentUrl,
        path,
        body: source === 'body' ? stream : {},
        bodySerializer: source === 'body' ? null : () => stream,
        duplex: 'half',
      }
      await expect(client.post(options)).rejects.toMatchObject({ status: 503 })
      expect(fetch).toHaveBeenCalledOnce()
    },
  )

  it('honors Retry-After seconds and dates without retrying early', async () => {
    vi.setSystemTime(new Date('2026-11-06T00:00:00Z'))
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockImplementationOnce(() => Promise.resolve(failure(429, '3')))
      .mockImplementationOnce(() => Promise.resolve(failure(503, 'Fri, 06 Nov 2026 00:00:08 GMT')))
      .mockResolvedValueOnce(Response.json({}))
    const done = createOmnaraClient({ baseUrl, fetch }).get({ url: '/agents' })
    await vi.advanceTimersByTimeAsync(2_999)
    expect(fetch).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(fetch).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(4_999)
    expect(fetch).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1)
    await done
    expect(fetch).toHaveBeenCalledTimes(3)
  })

  it('does not replay a caller-provided async iterable body', async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockImplementation(() => Promise.resolve(failure()))
    const client = createOmnaraClient({ baseUrl, fetch })
    async function* content() {
      yield new Uint8Array(await new Blob(['one-shot']).arrayBuffer())
    }
    const options = { url: agentUrl, path, body: content(), bodySerializer: null, duplex: 'half' }
    await expect(client.post(options)).rejects.toMatchObject({ status: 503 })
    expect(fetch).toHaveBeenCalledOnce()
  })

  it.each([
    { retryAfter: '61', timeoutMs: 0 },
    { retryAfter: '10', timeoutMs: 5_000 },
  ])(
    'returns the real API error if a server wait exceeds the allowed budget: %j',
    async ({ retryAfter, timeoutMs }) => {
      const fetch = vi
        .fn<typeof globalThis.fetch>()
        .mockImplementation(() => Promise.resolve(failure(429, retryAfter)))
      await expect(
        createOmnaraClient({ baseUrl, fetch, timeoutMs }).get({ url: '/agents' }),
      ).rejects.toMatchObject({ status: 429, retryAfterMs: Number(retryAfter) * 1_000 })
      expect(fetch).toHaveBeenCalledOnce()
      expect(vi.getTimerCount()).toBe(0)
    },
  )

  it('cancels a discarded response body', async () => {
    const cancel = vi.fn()
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(new Response(new ReadableStream({ cancel }), { status: 503 }))
      .mockResolvedValueOnce(Response.json({}))
    const done = createOmnaraClient({ baseUrl, fetch }).get({ url: '/agents' })
    await vi.runAllTimersAsync()
    await done
    expect(cancel).toHaveBeenCalledOnce()
  })

  it('supports client and request overrides without replacing a custom fetch', async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockImplementation(() => Promise.resolve(failure()))
    const client = createOmnaraClient({ baseUrl, fetch, maxRetries: 0 })
    await expect(client.get({ url: '/agents' })).rejects.toBeInstanceOf(ApiError)
    const customFetch = vi
      .fn<typeof globalThis.fetch>()
      .mockImplementation(() => Promise.resolve(failure()))
    const done = expect(
      client.get({ url: '/agents', fetch: customFetch, maxRetries: 1 }),
    ).rejects.toBeInstanceOf(ApiError)
    await vi.runAllTimersAsync()
    await done
    expect(fetch).toHaveBeenCalledOnce()
    expect(customFetch).toHaveBeenCalledTimes(2)
    client.setConfig({ maxRetries: 4 })
    await expect(client.get({ url: '/agents', maxRetries: 0 })).rejects.toBeInstanceOf(ApiError)
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('preserves non-throwing fields and data response modes on exhaustion', async () => {
    const client = createOmnaraClient({ baseUrl, fetch: () => Promise.resolve(failure()) })
    const fields = client.get({ url: '/agents', throwOnError: false })
    await vi.runAllTimersAsync()
    const result = await fields
    expect(result.error).toBeInstanceOf(ApiError)
    expect(result.request).toBeInstanceOf(Request)
    expect(result.response?.status).toBe(503)
    const data = client.get({ url: '/agents', throwOnError: false, responseStyle: 'data' })
    await vi.runAllTimersAsync()
    expect(await data).toBeUndefined()
  })

  it.each(['auth', 'request validation', 'response validation'] as const)(
    'does not retry a TypeError from %s',
    async (stage) => {
      const error = new TypeError('programming failure')
      const fail = () => Promise.reject(error)
      const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json({}))
      const client = createOmnaraClient({
        baseUrl,
        fetch,
        auth: stage === 'auth' ? { authenticate: fail } : undefined,
      })
      await expect(
        client.get({
          url: '/agents',
          requestValidator: stage === 'request validation' ? fail : undefined,
          responseValidator: stage === 'response validation' ? fail : undefined,
        }),
      ).rejects.toBe(error)
      expect(fetch).toHaveBeenCalledTimes(stage === 'response validation' ? 1 : 0)
    },
  )
})
