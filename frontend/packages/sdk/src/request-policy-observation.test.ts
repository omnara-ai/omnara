import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { createOmnaraClient } from './client'
import { listProjectToolCalls } from './generated/sdk.gen'
import type { RequestObservation } from './request-policy-options'

const baseUrl = 'https://api.example.test'

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
  vi.spyOn(Math, 'random').mockReturnValue(0.5)
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

it('reports one bounded observation for auth, retries, and server-directed backoff', async () => {
  const observations: RequestObservation[] = []
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(Response.json({}, { status: 429, headers: { 'Retry-After': '2' } }))
    .mockResolvedValueOnce(Response.json({}, { status: 201 }))
  const client = createOmnaraClient({
    baseUrl,
    fetch,
    timeoutMs: 10_000,
    auth: { authenticate: () => new Promise((resolve) => setTimeout(resolve, 40)) },
    onRequestComplete: (observation) => {
      observations.push(observation)
    },
  })
  const result = client.post({
    url: '/orgs/{orgID}/projects/{projectID}/agents',
    path: { orgID: 'private-org', projectID: 'private-project' },
    headers: { Authorization: 'Bearer private-token' },
    body: { private: 'payload' },
  })
  await vi.runAllTimersAsync()
  await result
  expect(observations).toEqual([
    {
      operation: 'POST /orgs/{orgID}/projects/{projectID}/agents',
      durationMs: 2040,
      attempts: 2,
      backoffMs: 2000,
      outcome: 'returned',
      status: 201,
    },
  ])
  expect(JSON.stringify(observations)).not.toContain('private')
})

it('labels generated reads by their API template without exposing identifiers or query values', async () => {
  const observations: RequestObservation[] = []
  const client = createOmnaraClient({
    baseUrl,
    fetch: () => Promise.resolve(Response.json({ data: [], next_cursor: null })),
    onRequestComplete: (observation) => {
      observations.push(observation)
    },
  })
  await listProjectToolCalls({
    client,
    path: { orgID: 'private-org', projectID: 'private-project' },
    query: { cursor: 'private-cursor' },
  })
  expect(observations).toEqual([
    {
      operation: 'GET /orgs/{orgID}/projects/{projectID}/tool-calls',
      durationMs: 0,
      attempts: 1,
      backoffMs: 0,
      outcome: 'returned',
      status: 200,
    },
  ])
  expect(JSON.stringify(observations)).not.toContain('private')
})

it('labels known unsafe writes without adding retries or idempotency keys', async () => {
  const onRequestComplete = vi.fn()
  const fetch = vi.fn<typeof globalThis.fetch>((request) => {
    expect(new Request(request).headers.has('Idempotency-Key')).toBe(false)
    return Promise.resolve(Response.json({}, { status: 503 }))
  })
  const client = createOmnaraClient({ baseUrl, fetch, onRequestComplete })
  const done = expect(
    client.post({
      url: '/orgs/{orgID}/projects/{projectID}/memory-stores',
      path: { orgID: 'private-org', projectID: 'private-project' },
    }),
  ).rejects.toMatchObject({ status: 503 })
  await vi.runAllTimersAsync()
  await done
  expect(fetch).toHaveBeenCalledOnce()
  expect(onRequestComplete).toHaveBeenCalledExactlyOnceWith({
    operation: 'POST /orgs/{orgID}/projects/{projectID}/memory-stores',
    durationMs: 0,
    attempts: 1,
    backoffMs: 0,
    outcome: 'threw',
    status: 503,
  })
})

it.each([true, false])(
  'reports actual promise settlement with throwOnError=%s',
  async (throwOnError) => {
    const onRequestComplete = vi.fn()
    const client = createOmnaraClient({
      baseUrl,
      maxRetries: 0,
      onRequestComplete,
      fetch: () => Promise.resolve(Response.json({ error: 'private message' }, { status: 503 })),
    })
    const result = client.get({ url: '/private-org?token=private-token', throwOnError })
    if (throwOnError) await expect(result).rejects.toMatchObject({ status: 503 })
    else await expect(result).resolves.toMatchObject({ error: { status: 503 } })
    expect(onRequestComplete).toHaveBeenCalledExactlyOnceWith({
      operation: 'unknown',
      durationMs: 0,
      attempts: 1,
      backoffMs: 0,
      outcome: throwOnError ? 'threw' : 'returned',
      status: 503,
    })
  },
)

it('reports an interrupted wait and preserves the caller cancellation reason', async () => {
  const source = new AbortController()
  const reason = { caller: 'stopped' }
  const onRequestComplete = vi.fn()
  const client = createOmnaraClient({
    baseUrl,
    onRequestComplete,
    fetch: () =>
      Promise.resolve(Response.json({}, { status: 429, headers: { 'Retry-After': '5' } })),
  })
  const done = expect(client.get({ url: '/test', signal: source.signal })).rejects.toBe(reason)
  await vi.advanceTimersByTimeAsync(100)
  source.abort(reason)
  await done
  expect(onRequestComplete).toHaveBeenCalledExactlyOnceWith({
    operation: 'unknown',
    durationMs: 100,
    attempts: 1,
    backoffMs: 100,
    outcome: 'canceled',
    status: 429,
  })
})

it('reports a total deadline without waiting for a stuck custom fetch', async () => {
  const onRequestComplete = vi.fn()
  const client = createOmnaraClient({
    baseUrl,
    timeoutMs: 10,
    onRequestComplete,
    fetch: () => new Promise<Response>(() => undefined),
  })
  const result = client.get({ url: '/test', throwOnError: false })
  await vi.advanceTimersByTimeAsync(10)
  await expect(result).resolves.toMatchObject({ error: { name: 'TimeoutError' } })
  expect(onRequestComplete).toHaveBeenCalledExactlyOnceWith({
    operation: 'unknown',
    durationMs: 10,
    attempts: 1,
    backoffMs: 0,
    outcome: 'timeout',
    status: undefined,
  })
})

it('permits per-call overrides and setConfig without multiplying observations', async () => {
  const original = vi.fn()
  const override = vi.fn()
  const configured = vi.fn()
  const client = createOmnaraClient({
    baseUrl,
    onRequestComplete: original,
    fetch: () => Promise.resolve(Response.json({ ok: true })),
  })
  await client.get({ url: '/test', onRequestComplete: override })
  expect(original).not.toHaveBeenCalled()
  expect(override).toHaveBeenCalledOnce()
  client.setConfig({ onRequestComplete: configured })
  await client.get({ url: '/test' })
  expect(configured).toHaveBeenCalledOnce()
})

it.each(['throw', 'reject', 'pending'] as const)(
  'contains an observer that will %s',
  async (behavior) => {
    const client = createOmnaraClient({
      baseUrl,
      fetch: () => Promise.resolve(Response.json({ ok: true })),
      onRequestComplete: () => {
        if (behavior === 'throw') throw new Error('observer failed')
        if (behavior === 'reject') return Promise.reject(new Error('observer failed'))
        return new Promise<void>(() => undefined)
      },
    })
    await expect(client.get({ url: '/test' })).resolves.toMatchObject({ data: { ok: true } })
  },
)
