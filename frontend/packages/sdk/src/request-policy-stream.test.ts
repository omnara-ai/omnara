import { afterEach, expect, it, vi } from 'vitest'

import { trackAbortListeners } from './abort-test-support'
import { createOmnaraClient } from './client'

const baseUrl = 'https://api.example.test'

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

function streamFixture() {
  const source = new AbortController()
  const listeners = trackAbortListeners(source.signal)
  const cancel = vi.fn()
  let controller!: ReadableStreamDefaultController<Uint8Array>
  const body = new ReadableStream<Uint8Array>({
    start(value) {
      controller = value
    },
    cancel,
  })
  let requestSignal: AbortSignal | undefined
  const client = createOmnaraClient({
    baseUrl,
    timeoutMs: 10,
    fetch: (input) => {
      requestSignal = new Request(input).signal
      const response = new Response(body, {
        status: 206,
        statusText: 'Partial Content',
        headers: { 'X-Test': 'stream' },
      })
      Object.defineProperties(response, {
        url: { value: `${baseUrl}/stream` },
        redirected: { value: true },
        type: { value: 'cors' },
      })
      return Promise.resolve(response)
    },
  })
  return { client, source, listeners, cancel, controller, requestSignal: () => requestSignal }
}

it('does not retry failed stream connections or leave a deadline timer behind', async () => {
  vi.useFakeTimers()
  vi.spyOn(Math, 'random').mockReturnValue(0.5)
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockImplementation(() =>
      Promise.resolve(Response.json({ error: 'unavailable' }, { status: 503 })),
    )
  const client = createOmnaraClient({ baseUrl, fetch, timeoutMs: 60_000 })
  const done = expect(client.get({ url: '/stream', parseAs: 'stream' })).rejects.toMatchObject({
    status: 503,
  })
  // Allow both retry backoffs to elapse without letting the deadline suppress a retry.
  await vi.advanceTimersByTimeAsync(3_000)
  await done
  expect(fetch).toHaveBeenCalledOnce()
  expect(vi.getTimerCount()).toBe(0)
})

it.each(['stream', 'auto'] as const)(
  'keeps %s parsing linked after headers, clears the deadline, and disposes at EOF',
  async (parseAs) => {
    vi.useFakeTimers()
    const { client, source, listeners, controller, requestSignal } = streamFixture()
    const result = await client.get<ReadableStream<Uint8Array>, unknown, true>({
      url: '/stream',
      parseAs,
      signal: source.signal,
      throwOnError: true,
    })
    expect(result.data).toBe(result.response.body)
    expect(result.response.headers.get('X-Test')).toBe('stream')
    expect(result.response).toMatchObject({
      url: `${baseUrl}/stream`,
      status: 206,
      statusText: 'Partial Content',
      redirected: true,
      type: 'cors',
    })
    expect(listeners.size).toBe(1)
    await vi.advanceTimersByTimeAsync(100)
    expect(requestSignal()?.aborted).toBe(false)
    const content = result.response.text()
    controller.enqueue(new TextEncoder().encode('first'))
    controller.enqueue(new TextEncoder().encode('last'))
    controller.close()
    expect(await content).toBe('firstlast')
    expect(listeners.size).toBe(0)
    source.abort()
    expect(requestSignal()?.aborted).toBe(false)
  },
)

it.each(['stream', 'auto'] as const)(
  'disposes a raw data-mode %s stream when canceled',
  async (parseAs) => {
    const { client, source, listeners, cancel } = streamFixture()
    const stream = await client.get<ReadableStream<Uint8Array>, unknown, true, 'data'>({
      url: '/stream',
      parseAs,
      signal: source.signal,
      throwOnError: true,
      responseStyle: 'data',
    })
    expect(listeners.size).toBe(1)
    const reason = { consumer: 'finished' }
    await stream.cancel(reason)
    expect(cancel).toHaveBeenCalledExactlyOnceWith(reason)
    expect(listeners.size).toBe(0)
  },
)

it('disposes an errored body even when the consumer has not started reading', async () => {
  const { client, source, listeners, controller } = streamFixture()
  const result = await client.get({ url: '/stream', parseAs: 'stream', signal: source.signal })
  const error = new Error('body disconnected')
  controller.error(error)
  await expect(result.response?.text()).rejects.toBe(error)
  expect(listeners.size).toBe(0)
})

it('disposes an empty completed body without requiring the consumer to read it', async () => {
  const { client, source, listeners, controller, requestSignal } = streamFixture()
  const result = await client.get({ url: '/stream', parseAs: 'stream', signal: source.signal })
  controller.close()
  await Promise.resolve()
  expect(listeners.size).toBe(0)
  source.abort()
  expect(requestSignal()?.aborted).toBe(false)
  await expect(result.response?.text()).resolves.toBe('')
})

it.each(['throw', 'data', 'fields'] as const)(
  'keeps body ownership explicit after an interceptor rejects in %s mode',
  async (mode) => {
    const { client, source, listeners, cancel, controller } = streamFixture()
    const reason = new Error('application rejected the response')
    client.interceptors.response.use(() => {
      throw reason
    })
    const result = client.get({
      url: '/stream',
      parseAs: 'stream',
      signal: source.signal,
      throwOnError: mode === 'throw',
      responseStyle: mode === 'data' ? 'data' : 'fields',
    })
    if (mode === 'throw') await expect(result).rejects.toBe(reason)
    else if (mode === 'data') await expect(result).resolves.toBeUndefined()
    else {
      const returned = await result
      expect(returned).toMatchObject({ error: reason })
      expect(cancel).not.toHaveBeenCalled()
      controller.enqueue(new TextEncoder().encode('available for inspection'))
      controller.close()
      await expect(returned.response?.text()).resolves.toBe('available for inspection')
    }
    expect(listeners.size).toBe(0)
    expect(cancel).toHaveBeenCalledTimes(mode === 'fields' ? 0 : 1)
  },
)

it.each(['throw', 'data', 'fields'] as const)(
  'preserves only handed-off HTTP error bodies in %s mode',
  async (mode) => {
    const source = new AbortController()
    const listeners = trackAbortListeners(source.signal)
    const body = { code: 'unauthorized', error: 'Session expired' }
    const response = Response.json(body, { status: 401 })
    const client = createOmnaraClient({ baseUrl, fetch: () => Promise.resolve(response) })
    const result = client.get({
      url: '/stream',
      parseAs: 'stream',
      signal: source.signal,
      throwOnError: mode === 'throw',
      responseStyle: mode === 'data' ? 'data' : 'fields',
    })
    if (mode === 'throw')
      await expect(result).rejects.toMatchObject({ status: 401, code: 'unauthorized' })
    else if (mode === 'data') await expect(result).resolves.toBeUndefined()
    else {
      await expect(result).resolves.toMatchObject({
        response,
        error: { status: 401, code: 'unauthorized' },
      })
      expect(response.bodyUsed).toBe(false)
      await expect(response.clone().json()).resolves.toEqual(body)
      await response.body?.cancel()
    }
    expect(listeners.size).toBe(0)
    expect(response.bodyUsed).toBe(true)
  },
)

it.each([true, false])(
  'keeps a deadline while reading an HTTP error body with throwOnError=%s',
  async (throwOnError) => {
    vi.useFakeTimers()
    const source = new AbortController()
    const listeners = trackAbortListeners(source.signal)
    let body!: ReadableStreamDefaultController<Uint8Array>
    const response = new Response(
      new ReadableStream<Uint8Array>({
        start(controller) {
          body = controller
        },
      }),
      { status: 503 },
    )
    const client = createOmnaraClient({
      baseUrl,
      timeoutMs: 10,
      fetch: () => Promise.resolve(response),
    })
    const result = client.get({
      url: '/stream',
      parseAs: 'stream',
      signal: source.signal,
      throwOnError,
    })
    const settled = throwOnError
      ? expect(result).rejects.toMatchObject({ name: 'TimeoutError' })
      : expect(result).resolves.toMatchObject({ error: { name: 'TimeoutError' } })
    await vi.advanceTimersByTimeAsync(10)
    await settled
    expect(listeners.size).toBe(0)
    expect(response.bodyUsed).toBe(true)
    // Finish the deliberately non-cooperative custom body after the caller has stopped waiting.
    body.close()
  },
)

it('preserves caller abort identity after headers even when custom fetch ignores cancellation', async () => {
  const { client, source, listeners, cancel, requestSignal } = streamFixture()
  const result = await client.get({ url: '/stream', parseAs: 'stream', signal: source.signal })
  const reason = { caller: 'stopped' }
  const content = expect(result.response?.text()).rejects.toBe(reason)
  source.abort(reason)
  await content
  expect(requestSignal()?.reason).toBe(reason)
  expect(cancel).toHaveBeenCalledExactlyOnceWith(reason)
  expect(listeners.size).toBe(0)
})

it('keeps cancellation attached when a response interceptor returns a clone', async () => {
  const { client, source, listeners, requestSignal } = streamFixture()
  client.interceptors.response.use((response) => response.clone())
  const result = await client.get({ url: '/stream', parseAs: 'stream', signal: source.signal })
  expect(listeners.size).toBe(1)
  const reason = { caller: 'stopped' }
  const content = expect(result.response?.text()).rejects.toBe(reason)
  source.abort(reason)
  await content
  expect(requestSignal()?.reason).toBe(reason)
  expect(listeners.size).toBe(0)
})

it('preserves network metadata across native clones and releases their shared stream', async () => {
  const { client, source, listeners, controller } = streamFixture()
  const result = await client.get<ReadableStream<Uint8Array>, unknown, true>({
    url: '/stream',
    parseAs: 'stream',
    signal: source.signal,
    throwOnError: true,
  })
  const reader = result.data.getReader()
  expect(result.response.bodyUsed).toBe(false)
  expect(() => result.response.clone()).toThrow(TypeError)
  reader.releaseLock()

  const clone = result.response.clone()
  const nestedClone = clone.clone()
  const responses = [result.response, clone, nestedClone]
  for (const response of responses) {
    expect(response).toBeInstanceOf(Response)
    expect(response).toMatchObject({
      url: `${baseUrl}/stream`,
      status: 206,
      statusText: 'Partial Content',
      redirected: true,
      type: 'cors',
      bodyUsed: false,
    })
    expect(response.headers.get('X-Test')).toBe('stream')
  }
  expect(listeners.size).toBe(1)
  const originalContent = result.response.text()
  expect(result.response.bodyUsed).toBe(true)
  expect(clone.bodyUsed).toBe(false)
  expect(() => result.response.clone()).toThrow(TypeError)
  const content = Promise.all([originalContent, clone.text(), nestedClone.text()])
  controller.enqueue(new TextEncoder().encode('shared body'))
  controller.close()
  await expect(content).resolves.toEqual(['shared body', 'shared body', 'shared body'])
  expect(listeners.size).toBe(0)
  for (const response of responses) {
    expect(response.bodyUsed).toBe(true)
    expect(() => response.clone()).toThrow(TypeError)
  }
})

it.each(['data', 'fields'] as const)(
  'keeps automatic %s stream parsing cancellable after headers',
  async (responseStyle) => {
    const { client, source, listeners, cancel, requestSignal } = streamFixture()
    const result = await client.get<
      ReadableStream<Uint8Array>,
      unknown,
      true,
      typeof responseStyle
    >({ url: '/stream', parseAs: 'auto', responseStyle, signal: source.signal, throwOnError: true })
    const stream = result instanceof ReadableStream ? result : result.data
    const reader = stream.getReader()
    const reason = new Error('caller stopped automatic stream')
    const read = expect(reader.read()).rejects.toBe(reason)
    expect(listeners.size).toBe(1)
    source.abort(reason)
    await read
    expect(requestSignal()?.reason).toBe(reason)
    expect(cancel).toHaveBeenCalledExactlyOnceWith(reason)
    expect(listeners.size).toBe(0)
  },
)

it('releases the caller link when an automatic data-mode stream ends normally', async () => {
  const { client, source, listeners, controller, requestSignal } = streamFixture()
  const stream = await client.get<ReadableStream<Uint8Array>, unknown, true, 'data'>({
    url: '/stream',
    parseAs: 'auto',
    responseStyle: 'data',
    signal: source.signal,
    throwOnError: true,
  })
  expect(listeners.size).toBe(1)
  const text = new Response(stream).text()
  controller.enqueue(new TextEncoder().encode('complete'))
  controller.close()
  expect(await text).toBe('complete')
  expect(listeners.size).toBe(0)
  source.abort()
  expect(requestSignal()?.aborted).toBe(false)
})
