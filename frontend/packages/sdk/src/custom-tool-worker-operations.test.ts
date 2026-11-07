import { afterEach, describe, expect, it, vi } from 'vitest'

import { createCustomToolWorker } from './custom-tool-worker'
import {
  accepted,
  api,
  failed,
  found,
  path,
  ready,
  toolCall,
  toolCallID,
  toolPage,
} from './custom-tool-worker-test-support'
import type { CustomToolHandler, CustomToolHandlerResult } from './custom-tool-worker-types'

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

const nextID = `tcl_${'b'.repeat(26)}`
const unavailable = () =>
  Response.json({ code: 'service_unavailable', error: 'unavailable' }, { status: 503 })

function fakeTime() {
  vi.useFakeTimers({
    toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'],
  })
  vi.spyOn(Math, 'random').mockReturnValue(0.5)
}

describe('custom tool operations', () => {
  it('reads authoritative state and input before handling a notification', async () => {
    let completed = true
    const { client, requests } = api(
      (_, body) => accepted(body ?? found),
      (request) =>
        Response.json({
          ...toolCall(new URL(request.url).pathname.split('/').at(-1)),
          state: completed ? 'completed' : 'ready',
        }),
    )
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: lookup },
      onError: vi.fn(),
    })
    expect(await worker.handle({ ...ready, input: undefined })).toEqual({ status: 'completed' })
    expect(lookup).not.toHaveBeenCalled()
    completed = false
    expect(await worker.handle({ ...ready, tool_call_id: nextID, input: undefined })).toEqual({
      status: 'completed',
    })
    expect(lookup).toHaveBeenCalledOnce()
    expect(requests.filter((request) => request.startsWith('GET '))).toHaveLength(2)
  })

  it('rechecks deferred jobs using list inputs and submits only once their saved result is ready', async () => {
    const calls = [toolCall(), toolCall(nextID)]
    const { client, requests, submitted } = api((request, body) =>
      body === undefined ? toolPage(request, calls) : accepted(body),
    )
    let saved = false
    const lookup = vi.fn<CustomToolHandler>(() =>
      Promise.resolve(saved ? found : { outcome: 'deferred' }),
    )
    const worker = createCustomToolWorker({
      client,
      path,
      concurrency: 1,
      tools: { lookup_customer: lookup },
      onError: vi.fn(),
    })
    expect((await worker.check({ limit: 2 })).results.map(({ outcome }) => outcome.status)).toEqual(
      ['deferred', 'deferred'],
    )
    expect(submitted).toEqual([])
    expect((await worker.check({ limit: 2 })).nextCursor).toBeNull()
    expect(lookup).toHaveBeenCalledTimes(4)
    saved = true
    expect((await worker.check({ limit: 2 })).results.map(({ outcome }) => outcome.status)).toEqual(
      ['completed', 'completed'],
    )
    expect(submitted).toEqual([found, found])
    expect(
      requests
        .filter((request) => request.startsWith('GET '))
        .every((request) => request.includes('/tool-calls?')),
    ).toBe(true)
    await worker.shutdown()
  })

  it('bounds retries and retains a finished result for the next operation', async () => {
    fakeTime()
    let available = false
    const { client, submitted } = api((_, body) =>
      available ? accepted(body ?? found) : unavailable(),
    )
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: lookup },
      onError,
    })
    const done = worker.handle(ready)
    await vi.advanceTimersByTimeAsync(2_000)
    expect(await done).toMatchObject({ status: 'failed', error: { status: 503 } })
    expect(submitted).toHaveLength(3)
    expect(onError).toHaveBeenCalledOnce()
    available = true
    expect(await worker.handle(ready)).toEqual({ status: 'completed' })
    expect(lookup).toHaveBeenCalledOnce()
    expect(submitted).toEqual([found, found, found, found])
    await worker.shutdown()
  })

  it('returns promptly for a long Retry-After and never automatically retries it early', async () => {
    fakeTime()
    let available = false
    const { client, requests, submitted } = api((_, body) =>
      available
        ? accepted(body ?? found)
        : Response.json(
            { code: 'rate_limit_exceeded', error: 'slow down' },
            { status: 429, headers: { 'Retry-After': '120' } },
          ),
    )
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const worker = createCustomToolWorker({
      client,
      path,
      concurrency: 1,
      tools: { lookup_customer: lookup },
      onError: vi.fn(),
    })
    expect(await worker.handle(ready)).toMatchObject({
      status: 'failed',
      error: { status: 429, retryAfterMs: 120_000 },
    })
    expect(await worker.handle(ready)).toEqual({ status: 'busy' })
    const requestsBeforeCheck = [...requests]
    expect(await worker.check({ cursor: 'saved-cursor' })).toMatchObject({
      results: [{ outcome: { status: 'busy' } }],
      nextCursor: 'saved-cursor',
      blocked: true,
    })
    expect(requests).toEqual(requestsBeforeCheck)
    expect(submitted).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(119_999)
    expect(await worker.handle(ready)).toEqual({ status: 'busy' })
    expect(submitted).toHaveLength(1)
    available = true
    await vi.advanceTimersByTimeAsync(1)
    expect(await worker.handle(ready)).toEqual({ status: 'completed' })
    expect(lookup).toHaveBeenCalledOnce()
    await worker.shutdown()
  })

  it('returns at the caller deadline, holds the execution slot, and saves a late result without submitting it', async () => {
    const { client, submitted } = api((_, body) => accepted(body ?? found))
    const controller = new AbortController()
    let finish: ((value: CustomToolHandlerResult) => void) | undefined
    let handlerSignal: AbortSignal | undefined
    const lookup = vi.fn<CustomToolHandler>(({ signal }) => {
      handlerSignal = signal
      return new Promise((resolve) => {
        finish = resolve
      })
    })
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      concurrency: 1,
      tools: { lookup_customer: lookup },
      onError,
    })
    const done = worker.handle(ready, { signal: controller.signal })
    await vi.waitFor(() => {
      expect(finish).toBeDefined()
    })
    controller.abort(new Error('job deadline'))
    expect(await done).toEqual({ status: 'stopped' })
    expect(handlerSignal?.aborted).toBe(true)
    expect(await worker.handle({ ...ready, tool_call_id: nextID })).toEqual({ status: 'busy' })
    finish?.(found)
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(onError).not.toHaveBeenCalled()
    expect(submitted).toEqual([])
    expect(await worker.handle(ready)).toEqual({ status: 'completed' })
    expect(submitted).toEqual([found])
    expect(lookup).toHaveBeenCalledOnce()
    await worker.shutdown()
  })

  it('a duplicate caller can stop waiting without canceling the original operation', async () => {
    const { client, submitted } = api((_, body) => accepted(body ?? found))
    let finish: ((value: CustomToolHandlerResult) => void) | undefined
    let signal: AbortSignal | undefined
    const worker = createCustomToolWorker({
      client,
      path,
      tools: {
        lookup_customer: (call) => {
          signal = call.signal
          return new Promise((resolve) => {
            finish = resolve
          })
        },
      },
      onError: vi.fn(),
    })
    const original = worker.handle(ready)
    await vi.waitFor(() => {
      expect(finish).toBeDefined()
    })
    const controller = new AbortController()
    const duplicate = worker.handle(ready, { signal: controller.signal })
    controller.abort()
    expect(await duplicate).toEqual({ status: 'stopped' })
    expect(signal?.aborted).toBe(false)
    finish?.(found)
    expect(await original).toEqual({ status: 'completed' })
    expect(submitted).toEqual([found])
    await worker.shutdown()
  })

  it('joins a running notification once while recovery reaches later calls', async () => {
    const calls = [toolCall(), toolCall(nextID)]
    const { client, requests, submitted } = api((request, body) =>
      body === undefined ? toolPage(request, calls) : accepted(body),
    )
    let finish: ((value: CustomToolHandlerResult) => void) | undefined
    const lookup = vi.fn<CustomToolHandler>((call) =>
      call.toolCallID === toolCallID
        ? new Promise((resolve) => {
            finish = resolve
          })
        : Promise.resolve(found),
    )
    const worker = createCustomToolWorker({
      client,
      path,
      concurrency: 2,
      tools: { lookup_customer: lookup },
      onError: vi.fn(),
    })
    const handled = worker.handle(ready)
    await vi.waitFor(() => {
      expect(finish).toBeDefined()
    })
    const checked = worker.check({ limit: 2 })
    let done = false
    void checked.then(() => {
      done = true
    })
    await vi.waitFor(() => {
      expect(submitted).toEqual([found])
    })
    expect(done).toBe(false)
    expect(lookup.mock.calls.map(([call]) => call.toolCallID)).toEqual([toolCallID, nextID])
    finish?.(found)
    expect(await handled).toEqual({ status: 'completed' })
    expect(await checked).toMatchObject({
      results: [
        { call: { toolCallID }, outcome: { status: 'completed' } },
        { call: { toolCallID: nextID }, outcome: { status: 'completed' } },
      ],
      nextCursor: null,
      blocked: false,
    })
    expect(submitted).toEqual([found, found])
    expect(
      requests.filter((request) => request.startsWith('GET ') && !request.includes('/tool-calls?')),
    ).toHaveLength(1)
    expect(requests.filter((request) => request.includes('/tool-calls?'))).toHaveLength(2)
    expect(lookup).toHaveBeenCalledTimes(2)
    await worker.shutdown()
  })

  it('bounds a check waiting for capacity without shutting down the helper', async () => {
    const { client, submitted } = api((request, body) =>
      body === undefined ? toolPage(request, [toolCall(), toolCall(nextID)]) : accepted(body),
    )
    let finish: ((value: CustomToolHandlerResult) => void) | undefined
    const lookup = vi.fn<CustomToolHandler>(
      () =>
        new Promise((resolve) => {
          finish = resolve
        }),
    )
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      concurrency: 1,
      tools: { lookup_customer: lookup },
      onError,
    })
    const controller = new AbortController()
    const checked = worker.check({ signal: controller.signal })
    const rejected = expect(checked).rejects.toThrow('job deadline')
    await vi.waitFor(() => {
      expect(finish).toBeDefined()
    })
    controller.abort(new Error('job deadline'))
    await rejected
    expect(lookup).toHaveBeenCalledOnce()
    finish?.(found)
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(onError).not.toHaveBeenCalled()
    expect(submitted).toEqual([])
    expect(await worker.handle(ready)).toEqual({ status: 'completed' })
    await worker.shutdown()
  })

  it('recovers in run after an exhausted transient listing failure', async () => {
    fakeTime()
    let reads = 0
    const { client } = api((_, body) =>
      body !== undefined
        ? accepted(body)
        : ++reads <= 3
          ? unavailable()
          : Response.json({ data: [toolCall()], next_cursor: null }),
    )
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: lookup },
      onError,
    })
    const done = worker.run()
    await vi.advanceTimersByTimeAsync(2_000)
    expect(onError).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({ status: 503 }),
      undefined,
    )
    expect(lookup).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(lookup).toHaveBeenCalledOnce()
    await worker.shutdown()
    await done
  })

  it('preserves the list retry deadline when response processing times out', async () => {
    fakeTime()
    const startedAt = Date.now()
    const reads: number[] = []
    const { client, submitted } = api(async (request, body) => {
      if (body !== undefined) return accepted(body)
      reads.push(Date.now() - startedAt)
      if (reads.length > 1) return toolPage(request, [toolCall()])
      await new Promise((resolve) => setTimeout(resolve, 20_000))
      return new Response(
        new ReadableStream({
          async start(controller) {
            await new Promise((resolve) => setTimeout(resolve, 20_000))
            controller.enqueue(
              new TextEncoder().encode(
                JSON.stringify({ code: 'rate_limit_exceeded', error: 'slow down' }),
              ),
            )
            controller.close()
          },
        }),
        { status: 429, headers: { 'Retry-After': '120', 'Content-Type': 'application/json' } },
      )
    })
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: lookup },
      onError,
    })
    const running = worker.run()
    await vi.advanceTimersByTimeAsync(30_000)
    expect(onError).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({ name: 'TimeoutError' }),
      undefined,
    )
    await vi.advanceTimersByTimeAsync(109_999)
    expect(reads).toEqual([0])
    expect(lookup).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(2)
    expect(reads).toEqual([0, 140_001])
    expect(lookup).toHaveBeenCalledOnce()
    expect(submitted).toEqual([found])
    await worker.shutdown()
    await running
  })

  it('bounds stalled authentication by default and prevents a late dispatch', async () => {
    fakeTime()
    const { client, requests } = api((_, body) => accepted(body ?? found))
    let authenticate: (() => void) | undefined
    client.interceptors.request.use(async (request) => {
      await new Promise<void>((resolve) => {
        authenticate = resolve
      })
      return request
    })
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: () => Promise.resolve(found) },
      onError: vi.fn(),
    })
    const done = worker.handle(ready)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(await done).toMatchObject({ status: 'failed', error: { name: 'TimeoutError' } })
    authenticate?.()
    await vi.advanceTimersByTimeAsync(0)
    expect(requests).toEqual([])
  })

  it('does not expose thrown details or invalid handler results to the model', async () => {
    const { client, submitted } = api((_, body) => accepted(body ?? found))
    const onError = vi.fn()
    const secretError = new Error('postgres://password@private.internal')
    const worker = createCustomToolWorker({
      client,
      path,
      onError,
      tools: {
        lookup_customer: ({ toolCallID: id }): Promise<CustomToolHandlerResult> => {
          if (id === toolCallID) return Promise.reject(secretError)
          // @ts-expect-error Exercise a malformed result from a plain-JavaScript handler.
          return Promise.resolve({ outcome: 'succeeded', content_blocks: 'private internal data' })
        },
      },
    })
    expect(await worker.handle(ready)).toEqual({ status: 'completed' })
    expect(await worker.handle({ ...ready, tool_call_id: nextID })).toEqual({ status: 'completed' })
    expect(submitted).toEqual([failed(), failed()])
    expect(JSON.stringify(submitted)).not.toContain('private')
    expect(onError).toHaveBeenCalledWith(secretError, expect.objectContaining({ toolCallID }))
    expect(onError).toHaveBeenCalledTimes(2)
  })

  it('remembers a completion received before an older ready event', async () => {
    const { client, requests } = api(() => unavailable())
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: lookup },
      onError: vi.fn(),
    })
    await worker.handle({ ...ready, state: 'completed' })
    expect(await worker.handle(ready)).toEqual({ status: 'completed' })
    expect(lookup).not.toHaveBeenCalled()
    expect(requests).toEqual([])
  })

  it('waits for Retry-After during shutdown when it fits in the grace period', async () => {
    fakeTime()
    let posts = 0
    const { client } = api((_, body) =>
      ++posts <= 3
        ? Response.json(
            { code: 'rate_limit_exceeded', error: 'slow down' },
            { status: 429, headers: { 'Retry-After': '1' } },
          )
        : accepted(body ?? found),
    )
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const worker = createCustomToolWorker({
      client,
      path,
      shutdownGraceMs: 2_000,
      tools: { lookup_customer: lookup },
      onError: vi.fn(),
    })
    const handled = worker.handle(ready)
    await vi.advanceTimersByTimeAsync(2_000)
    expect(await handled).toMatchObject({ status: 'failed' })
    expect(posts).toBe(3)
    const shutdown = worker.shutdown()
    await vi.advanceTimersByTimeAsync(999)
    expect(posts).toBe(3)
    await vi.advanceTimersByTimeAsync(1)
    await shutdown
    expect(posts).toBe(4)
    expect(lookup).toHaveBeenCalledOnce()
  })

  it.each(['120', '2147484'])(
    'reports a retained result when Retry-After %s exceeds the shutdown grace',
    async (retryAfter) => {
      fakeTime()
      const { client, submitted } = api(() =>
        Response.json(
          { code: 'rate_limit_exceeded', error: 'slow down' },
          { status: 429, headers: { 'Retry-After': retryAfter } },
        ),
      )
      const onError = vi.fn()
      const worker = createCustomToolWorker({
        client,
        path,
        shutdownGraceMs: 1_000,
        tools: { lookup_customer: () => Promise.resolve(found) },
        onError,
      })
      expect(await worker.handle(ready)).toMatchObject({ status: 'failed' })
      const shutdown = worker.shutdown()
      await vi.advanceTimersByTimeAsync(1_000)
      await shutdown
      expect(submitted).toHaveLength(1)
      expect(onError).toHaveBeenLastCalledWith(
        expect.objectContaining({ message: 'custom tool shutdown grace period expired' }),
        expect.objectContaining({ toolCallID }),
      )
    },
  )

  it('preserves Retry-After when the operation is canceled while waiting to retry', async () => {
    fakeTime()
    let available = false
    const { client, submitted } = api((_, body) =>
      available
        ? accepted(body ?? found)
        : Response.json(
            { code: 'rate_limit_exceeded', error: 'slow down' },
            { status: 429, headers: { 'Retry-After': '10' } },
          ),
    )
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: () => Promise.resolve(found) },
      onError,
    })
    const controller = new AbortController()
    const done = worker.handle(ready, { signal: controller.signal })
    await vi.advanceTimersByTimeAsync(100)
    controller.abort()
    expect(await done).toEqual({ status: 'stopped' })
    await vi.advanceTimersByTimeAsync(0)
    available = true
    expect(await worker.handle(ready)).toEqual({ status: 'busy' })
    await vi.advanceTimersByTimeAsync(9_900)
    expect(await worker.handle(ready)).toEqual({ status: 'completed' })
    expect(submitted).toHaveLength(2)
    expect(onError).not.toHaveBeenCalled()
    await worker.shutdown()
  })

  it.each([401, 403, 404, 422])(
    'retains the same result after a %i rejection and submits it after access recovers',
    async (status) => {
      let allowed = false
      const { client, requests, submitted } = api((_, body) =>
        allowed
          ? accepted(body ?? found)
          : Response.json(
              { code: status === 404 ? 'not_found' : 'unrecognized_error', error: 'unavailable' },
              { status },
            ),
      )
      const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
      const worker = createCustomToolWorker({
        client,
        path,
        concurrency: 1,
        tools: { lookup_customer: lookup },
        onError: vi.fn(),
      })
      expect(await worker.handle(ready)).toMatchObject({ status: 'failed', error: { status } })
      expect(await worker.handle({ ...ready, tool_call_id: nextID })).toEqual({ status: 'busy' })
      allowed = true
      expect(await worker.handle(ready)).toEqual({ status: 'completed' })
      expect(lookup).toHaveBeenCalledOnce()
      expect(submitted).toEqual([found, found])
      expect(requests.filter((request) => request.startsWith('GET '))).toHaveLength(1)
      await worker.shutdown()
    },
  )

  it.each(['conflict', 'state_transition_conflict'])(
    'releases a result rejected as %s without permanently settling the call',
    async (code) => {
      let state = 'ready'
      const { client, submitted } = api(
        (_, body) => {
          if (submitted.length > 1) return accepted(body ?? found)
          state = 'awaiting_permission'
          return Response.json({ code, error: 'not ready' }, { status: 409 })
        },
        () => Response.json({ ...toolCall(), state }),
      )
      const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
      const worker = createCustomToolWorker({
        client,
        path,
        concurrency: 1,
        tools: { lookup_customer: lookup },
        onError: vi.fn(),
      })
      expect(await worker.handle(ready)).toMatchObject({ status: 'failed', error: { code } })
      expect(await worker.handle(ready)).toEqual({ status: 'ignored' })
      state = 'ready'
      expect(await worker.handle(ready)).toEqual({ status: 'completed' })
      expect(lookup).toHaveBeenCalledTimes(2)
      await worker.shutdown()
    },
  )

  it('supports more than 100 tool handlers by filtering list pages locally', async () => {
    let names: string | null | undefined
    const { client } = api((request, body) => {
      if (body !== undefined) return accepted(body)
      names = new URL(request.url).searchParams.get('names')
      return Response.json({
        data: [toolCall(), { ...toolCall(nextID), name: 'unregistered' }],
        next_cursor: null,
      })
    })
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const tools = Object.fromEntries(Array.from({ length: 100 }, (_, i) => [`tool_${i}`, lookup]))
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { ...tools, lookup_customer: lookup },
      onError: vi.fn(),
    })
    expect((await worker.check()).results).toHaveLength(1)
    expect(names).toBeNull()
    expect(lookup).toHaveBeenCalledOnce()
    await worker.shutdown()
  })

  it('allows a longer API budget for large results while keeping the operation bounded', async () => {
    fakeTime()
    const { client } = api(async (_, body) => {
      await new Promise((resolve) => setTimeout(resolve, 45_000))
      return accepted(body ?? found)
    })
    const worker = createCustomToolWorker({
      client,
      path,
      requestTimeoutMs: 60_000,
      tools: { lookup_customer: () => Promise.resolve(found) },
      onError: vi.fn(),
    })
    const done = worker.handle(ready)
    await vi.advanceTimersByTimeAsync(45_000)
    expect(await done).toEqual({ status: 'completed' })
    await worker.shutdown()
  })

  it('does not report an unfinished result if completion arrives during the shutdown retry wait', async () => {
    fakeTime()
    const { client } = api(() =>
      Response.json(
        { code: 'rate_limit_exceeded', error: 'slow down' },
        { status: 429, headers: { 'Retry-After': '120' } },
      ),
    )
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      shutdownGraceMs: 1_000,
      tools: { lookup_customer: () => Promise.resolve(found) },
      onError,
    })
    await worker.handle(ready)
    onError.mockClear()
    const shutdown = worker.shutdown()
    await vi.advanceTimersByTimeAsync(1)
    expect(await worker.handle({ ...ready, state: 'completed' })).toEqual({ status: 'completed' })
    await vi.advanceTimersByTimeAsync(999)
    await shutdown
    expect(onError).not.toHaveBeenCalled()
  })

  it('reaches every page when deferred calls take longer than one recovery interval', async () => {
    fakeTime()
    const deferred = Array.from({ length: 100 }, (_, i) => {
      const id = `tcl_${String.fromCharCode(97 + Math.floor(i / 26), 97 + (i % 26))}${'a'.repeat(24)}`
      return toolCall(id)
    })
    const last = toolCall(`tcl_${'z'.repeat(26)}`)
    const { client, submitted } = api((request, body) =>
      body === undefined ? toolPage(request, [...deferred, last]) : accepted(body),
    )
    const visited = new Set<string>()
    const worker = createCustomToolWorker({
      client,
      path,
      concurrency: 1,
      tools: {
        lookup_customer: async ({ toolCallID }) => {
          visited.add(toolCallID)
          await new Promise((resolve) => setTimeout(resolve, 1_000))
          return toolCallID === last.id ? found : { outcome: 'deferred' }
        },
      },
      onError: vi.fn(),
    })
    const run = worker.run()
    await vi.advanceTimersByTimeAsync(120_000)
    expect(visited).toEqual(new Set([...deferred.map((call) => call.id), last.id]))
    expect(submitted).toEqual([found])
    await worker.shutdown()
    await run
  })
})
