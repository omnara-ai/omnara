import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { trackAbortListeners } from './abort-test-support'
import { createOmnaraClient } from './client'

const baseUrl = 'https://api.example.test/v1'
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((accept, fail) => {
    resolve = accept
    reject = fail
  })
  return { promise, resolve, reject }
}
beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
  vi.spyOn(Math, 'random').mockReturnValue(0.5)
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

it.each([-1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1])(
  'rejects invalid retry policy values: %s',
  async (value) => {
    expect(() => createOmnaraClient({ maxRetries: value })).toThrow(RangeError)
    expect(() => createOmnaraClient({ timeoutMs: value })).toThrow(RangeError)
    const client = createOmnaraClient({ baseUrl })
    expect(() => client.setConfig({ maxRetries: value })).toThrow(RangeError)
    await expect(client.get({ url: '/test', timeoutMs: value })).rejects.toThrow(RangeError)
  },
)

it('rejects deadlines which would overflow the runtime timer', () => {
  expect(() => createOmnaraClient({ timeoutMs: 2_147_483_648 })).toThrow(RangeError)
})

it.each(['success', 'error', 'timeout'] as const)(
  'releases every caller link after repeated %s requests',
  async (completion) => {
    const source = new AbortController()
    const listeners = trackAbortListeners(source.signal)
    const client = createOmnaraClient({
      baseUrl,
      maxRetries: 0,
      timeoutMs: 10,
      fetch: () =>
        completion === 'timeout'
          ? new Promise<Response>(() => undefined)
          : Promise.resolve(Response.json({}, { status: completion === 'success' ? 200 : 503 })),
    })
    for (let request = 0; request < 5; request++) {
      const pending = client.get({ url: '/test', signal: source.signal })
      expect(listeners.size).toBe(1)
      const settled =
        completion === 'success'
          ? expect(pending).resolves.toMatchObject({ data: {} })
          : expect(pending).rejects.toBeInstanceOf(Error)
      await vi.advanceTimersByTimeAsync(10)
      await settled
      expect(listeners.size).toBe(0)
    }
  },
)

it('stops waiting for auth at the deadline and never dispatches late', async () => {
  const auth = deferred<undefined>()
  const fetch = vi.fn<typeof globalThis.fetch>()
  const client = createOmnaraClient({
    baseUrl,
    fetch,
    timeoutMs: 100,
    auth: { authenticate: () => auth.promise },
  })
  const done = expect(client.get({ url: '/test' })).rejects.toMatchObject({ name: 'TimeoutError' })
  await vi.advanceTimersByTimeAsync(100)
  await done
  auth.resolve(undefined)
  await vi.runAllTimersAsync()
  expect(fetch).not.toHaveBeenCalled()
})

it.each(['fetch', 'body'] as const)(
  'bounds time spent waiting for %s even when it ignores cancellation',
  async (stage) => {
    const response = deferred<Response>()
    const body = new ReadableStream<Uint8Array>()
    const fetch = vi.fn<typeof globalThis.fetch>(() =>
      stage === 'fetch'
        ? response.promise
        : Promise.resolve(new Response(body, { headers: { 'Content-Type': 'application/json' } })),
    )
    const client = createOmnaraClient({ baseUrl, fetch, timeoutMs: 100 })
    const done = expect(client.get({ url: '/test' })).rejects.toMatchObject({
      name: 'TimeoutError',
    })
    await vi.advanceTimersByTimeAsync(100)
    await done
    expect(fetch).toHaveBeenCalledOnce()
    response.reject(new TypeError('late rejection'))
    // In the body case the promise was never awaited; attach a rejection handler.
    if (stage === 'body') await response.promise.catch(() => undefined)
    await vi.runAllTimersAsync()
    expect(fetch).toHaveBeenCalledOnce()
  },
)

it.each(['before', 'auth', 'backoff', 'fetch'] as const)(
  'preserves the exact caller abort reason during %s',
  async (stage) => {
    const controller = new AbortController()
    const reason = new Error('caller stopped')
    const pendingAuth = deferred<undefined>()
    const fetch = vi.fn<typeof globalThis.fetch>(() =>
      stage === 'fetch'
        ? new Promise<Response>(() => undefined)
        : Promise.resolve(
            Response.json({ error: 'wait' }, { status: 429, headers: { 'Retry-After': '5' } }),
          ),
    )
    const client = createOmnaraClient({
      baseUrl,
      fetch,
      auth: stage === 'auth' ? { authenticate: () => pendingAuth.promise } : undefined,
    })
    if (stage === 'before') controller.abort(reason)
    const done = expect(client.get({ url: '/test', signal: controller.signal })).rejects.toBe(
      reason,
    )
    await vi.advanceTimersByTimeAsync(1)
    controller.abort(reason)
    await done
    pendingAuth.resolve(undefined)
    await vi.runAllTimersAsync()
    expect(fetch).toHaveBeenCalledTimes(stage === 'before' || stage === 'auth' ? 0 : 1)
  },
)

it('preserves non-throwing modes when the deadline wins', async () => {
  const client = createOmnaraClient({
    baseUrl,
    timeoutMs: 10,
    fetch: () => new Promise<Response>(() => undefined),
  })
  const fields = client.get({ url: '/test', throwOnError: false })
  await vi.advanceTimersByTimeAsync(10)
  const result = await fields
  expect(result.error).toMatchObject({ name: 'TimeoutError' })
  expect(result.request).toBeInstanceOf(Request)
  const data = client.get({ url: '/test', throwOnError: false, responseStyle: 'data' })
  await vi.advanceTimersByTimeAsync(10)
  expect(await data).toBeUndefined()
})

it('does not return a discarded response when canceled during backoff', async () => {
  const controller = new AbortController()
  const reason = new Error('stopped')
  const client = createOmnaraClient({
    baseUrl,
    fetch: () => Promise.resolve(Response.json({ error: 'wait' }, { status: 429 })),
  })
  const done = client.get({ url: '/test', signal: controller.signal, throwOnError: false })
  await vi.advanceTimersByTimeAsync(10)
  controller.abort(reason)
  const result = await done
  expect(result.error).toBe(reason)
  expect(result.response).toBeUndefined()
})

it('cancels a response that arrives after the caller has already stopped waiting', async () => {
  const response = deferred<Response>()
  const controller = new AbortController()
  const cancel = vi.fn()
  const client = createOmnaraClient({ baseUrl, fetch: () => response.promise })
  const done = expect(client.get({ url: '/test', signal: controller.signal })).rejects.toBe(
    'caller reason',
  )
  await vi.advanceTimersByTimeAsync(1)
  controller.abort('caller reason')
  await done
  response.resolve(new Response(new ReadableStream({ cancel })))
  await vi.runAllTimersAsync()
  expect(cancel).toHaveBeenCalledOnce()
})

it('allows a request to disable the client deadline', async () => {
  const response = deferred<Response>()
  const client = createOmnaraClient({ baseUrl, timeoutMs: 10, fetch: () => response.promise })
  const done = client.get({ url: '/test', timeoutMs: 0 })
  await vi.advanceTimersByTimeAsync(100)
  response.resolve(Response.json({ ok: true }))
  expect(await done).toMatchObject({ data: { ok: true } })
})

it('never retries streaming responses and stops the deadline when headers arrive', async () => {
  const signal = new AbortController()
  let requestSignal: AbortSignal | undefined
  const fetch = vi.fn<typeof globalThis.fetch>((input) => {
    requestSignal = new Request(input).signal
    return Promise.resolve(
      new Response(new ReadableStream(), { headers: { 'Content-Type': 'text/event-stream' } }),
    )
  })
  const client = createOmnaraClient({ baseUrl, fetch, timeoutMs: 10 })
  const response = await client.get({ url: '/stream', parseAs: 'stream', signal: signal.signal })
  expect(response.data).toBeInstanceOf(ReadableStream)
  await vi.advanceTimersByTimeAsync(100)
  expect(requestSignal?.aborted).toBe(false)
  signal.abort()
  expect(requestSignal?.aborted).toBe(true)
  fetch.mockResolvedValue(Response.json({ error: 'unavailable' }, { status: 503 }))
  await expect(client.get({ url: '/stream', parseAs: 'stream' })).rejects.toMatchObject({
    status: 503,
  })
  expect(fetch).toHaveBeenCalledTimes(2)
  expect(vi.getTimerCount()).toBe(0)
})
