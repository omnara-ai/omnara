import { afterEach, describe, expect, it, vi } from 'vitest'

import { createCustomToolWorker, type CustomToolHandler } from './custom-tool-worker'
import {
  accepted,
  agentID,
  api,
  callPath,
  failed,
  found,
  path,
  ready,
  routing,
  toolCall,
  toolCallID,
  toolPage,
} from './custom-tool-worker-test-support'
import type { SubmitToolCallResultRequest } from './generated/types.gen'

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('createCustomToolWorker', () => {
  it('does not report or submit a tool stopped by a shutdown it starts', async () => {
    const controller = new AbortController()
    const { client, requests } = api((_, body) =>
      body === undefined
        ? Response.json({ data: [toolCall()], next_cursor: null })
        : accepted(body),
    )
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      tools: {
        lookup_customer: ({ signal }) => {
          controller.abort()
          return signal.aborted ? Promise.reject(new Error('stopped')) : Promise.resolve(found)
        },
      },
      signal: controller.signal,
      onError,
    })

    await worker.check()
    expect(onError).not.toHaveBeenCalled()
    expect(requests).not.toContain(`POST ${callPath}/result`)
  })

  it('limits running tools, runs skipped calls as slots free, and waits for them on shutdown', async () => {
    const controller = new AbortController()
    const rankID = `tcl_${'b'.repeat(26)}`
    const finished = new Set<string>()
    const { client } = api((request, body) => {
      if (body !== undefined) {
        finished.add(/tool-calls\/([^/]+)\/result/.exec(request.url)?.[1] ?? '')
        return accepted(body)
      }
      const calls = [toolCall(), toolCall(rankID)].filter((call) => !finished.has(call.id))
      return toolPage(request, calls)
    })
    const release = new Map<string, (result: SubmitToolCallResultRequest) => void>()
    const lookup = vi.fn<CustomToolHandler>(
      (call) =>
        new Promise((resolve) => {
          release.set(call.toolCallID, resolve)
        }),
    )
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: lookup },
      concurrency: 1,
      signal: controller.signal,
      onError: vi.fn(),
    })

    const stopped = worker.run()
    await vi.waitFor(() => {
      expect(release.has(toolCallID)).toBe(true)
    })
    expect(lookup).toHaveBeenCalledTimes(1)

    release.get(toolCallID)?.(found)
    await vi.waitFor(() => {
      expect(release.has(rankID)).toBe(true)
    })
    const toolSignal = lookup.mock.calls[1]?.[0].signal

    let done = false
    void stopped.then(() => {
      done = true
    })
    controller.abort()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(done).toBe(false)
    expect(toolSignal?.aborted).toBe(true)
    release.get(rankID)?.(found)
    await stopped
    expect(lookup).toHaveBeenCalledTimes(2)
    expect(finished.has(rankID)).toBe(true)
  })

  it('reports a listing failure without stopping webhook handling', async () => {
    const { client } = api((_, body) =>
      body === undefined
        ? Response.json({ error: 'forbidden', code: 'forbidden' }, { status: 403 })
        : accepted(body),
    )
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: () => Promise.resolve(found) },
      onError,
    })
    const run = worker.run()
    await vi.waitFor(() => {
      expect(onError).toHaveBeenCalledExactlyOnceWith(
        expect.objectContaining({ status: 403 }),
        undefined,
      )
    })
    expect(await worker.handle(ready)).toEqual({ status: 'completed' })
    await worker.shutdown()
    await run
  })

  it('stops a running tool when its call is canceled', async () => {
    const { client, requests } = api(() => new Response(null, { status: 500 }))
    const onError = vi.fn()
    let started = false
    const worker = createCustomToolWorker({
      client,
      path,
      tools: {
        lookup_customer: ({ signal }) =>
          new Promise((_, reject) => {
            started = true
            signal.addEventListener('abort', () => {
              reject(new Error('stopped'))
            })
          }),
      },
      onError,
    })

    const done = worker.handle(ready)
    await vi.waitFor(() => {
      expect(started).toBe(true)
    })
    await worker.handle({
      ...routing,
      tool_call_id: toolCallID,
      agent_id: agentID,
      state: 'completed',
    })
    await done

    expect(onError).not.toHaveBeenCalled()
    expect(requests).toEqual([`GET ${callPath}`])
  })

  it('bounds the entire shutdown and never submits after its deadline', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    vi.spyOn(Math, 'random').mockReturnValue(0)
    vi.spyOn(AbortSignal, 'timeout').mockImplementation((ms) => {
      const timeout = new AbortController()
      setTimeout(() => {
        timeout.abort()
      }, ms)
      return timeout.signal
    })
    const controller = new AbortController()
    const lateID = `tcl_${'b'.repeat(26)}`
    let latePosts = 0
    const { client, requests } = api((request, body) => {
      if (body === undefined) {
        return Response.json({ data: [toolCall(), toolCall(lateID)], next_cursor: null })
      }
      if (request.url.includes(lateID) && ++latePosts === 1) {
        return Response.json({ error: 'unavailable', code: 'service_unavailable' }, { status: 503 })
      }
      return new Promise((_, reject) => {
        request.signal.addEventListener('abort', () => {
          reject(new Error('aborted'))
        })
      })
    })
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      tools: {
        lookup_customer: ({ toolCallID: id }) =>
          new Promise((resolve) => {
            setTimeout(
              () => {
                resolve(found)
              },
              id === lateID ? 20_000 : 0,
            )
          }),
      },
      signal: controller.signal,
      onError,
    })

    const stopped = worker.run()
    await vi.waitFor(() => {
      expect(requests).toContain(`POST ${callPath}/result`)
    })
    controller.abort()
    await vi.advanceTimersByTimeAsync(10_000)
    await stopped
    expect(latePosts).toBe(0)
    expect(onError).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(20_000)
    expect(latePosts).toBe(0)
    expect(onError).toHaveBeenCalledTimes(2)
  })

  it('flushes a previously retained result during shutdown without rerunning the tool', async () => {
    let available = false
    const { client, submitted } = api((_, body) =>
      available
        ? accepted(body ?? found)
        : Response.json({ code: 'forbidden', error: 'forbidden' }, { status: 403 }),
    )
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const worker = createCustomToolWorker({
      client,
      path,
      onError: vi.fn(),
      tools: { lookup_customer: lookup },
    })
    expect(await worker.handle(ready)).toMatchObject({ status: 'failed' })
    available = true
    await worker.shutdown()
    expect(lookup).toHaveBeenCalledOnce()
    expect(submitted).toEqual([found, found])
    expect(await worker.handle(ready)).toEqual({ status: 'stopped' })
    await expect(worker.check()).rejects.toThrow()
  })

  it('reports result submission failure during shutdown to the caller and onError', async () => {
    let finish: ((result: SubmitToolCallResultRequest) => void) | undefined
    const { client } = api(() =>
      Response.json({ code: 'forbidden', error: 'forbidden' }, { status: 403 }),
    )
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      onError,
      tools: {
        lookup_customer: () =>
          new Promise((resolve) => {
            finish = resolve
          }),
      },
    })
    const done = worker.handle(ready)
    await vi.waitFor(() => {
      expect(finish).toBeDefined()
    })
    const stopped = worker.shutdown()
    finish?.(found)
    expect(await done).toMatchObject({ status: 'failed', error: { status: 403 } })
    await stopped
    expect(onError).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({ status: 403 }),
      expect.objectContaining({ toolCallID }),
    )
  })

  it('flushes a retained result when shutdown begins inside its error callback', async () => {
    let available = false
    const { client, submitted } = api((_, body) =>
      available
        ? accepted(body ?? found)
        : Response.json({ code: 'forbidden', error: 'forbidden' }, { status: 403 }),
    )
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: lookup },
      onError: () => {
        available = true
        void worker.shutdown()
      },
    })
    expect(await worker.handle(ready)).toMatchObject({ status: 'failed' })
    await worker.shutdown()
    expect(lookup).toHaveBeenCalledOnce()
    expect(submitted).toEqual([found, found])
  })

  it.each(['list', 'result'])(
    'bounds shutdown with pending %s authentication and prevents a late request',
    async (kind) => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      let authenticate: (() => void) | undefined
      const { client, requests } = api((_, body) =>
        body === undefined
          ? Response.json({ data: [toolCall()], next_cursor: null })
          : accepted(body),
      )
      client.interceptors.request.use(async (request) => {
        if (
          request.url.endsWith(
            kind === 'list'
              ? '/tool-calls?state=ready&type=custom&names=lookup_customer&limit=10'
              : '/result',
          )
        ) {
          await new Promise<void>((resolve) => {
            authenticate = resolve
          })
        }
        return request
      })
      const onError = vi.fn()
      const worker = createCustomToolWorker({
        client,
        path,
        shutdownGraceMs: 100,
        onError,
        tools: { lookup_customer: () => Promise.resolve(found) },
      })
      const done = kind === 'list' ? worker.run() : worker.handle(ready)
      await vi.waitFor(() => {
        expect(authenticate).toBeDefined()
      })
      const stopped = worker.shutdown()
      await vi.advanceTimersByTimeAsync(100)
      await stopped
      await done
      const requestCount = requests.length
      const errorCount = onError.mock.calls.length
      authenticate?.()
      await vi.advanceTimersByTimeAsync(0)
      expect(requests).toHaveLength(requestCount)
      expect(onError).toHaveBeenCalledTimes(errorCount)
      expect(errorCount).toBe(kind === 'list' ? 0 : 1)
    },
  )

  it('keeps a canceled handler within the concurrency limit until it cooperates', async () => {
    let finish: ((result: SubmitToolCallResultRequest) => void) | undefined
    const { client, submitted } = api((_, body) => accepted(body ?? found))
    const worker = createCustomToolWorker({
      client,
      path,
      concurrency: 1,
      onError: vi.fn(),
      tools: {
        lookup_customer: () =>
          new Promise((resolve) => {
            finish = resolve
          }),
      },
    })
    const done = worker.handle(ready)
    await vi.waitFor(() => {
      expect(finish).toBeDefined()
    })
    const completed = worker.handle({ ...ready, state: 'completed' })
    expect(await worker.handle({ ...ready, tool_call_id: `tcl_${'b'.repeat(26)}` })).toEqual({
      status: 'busy',
    })
    finish?.(found)
    expect(await done).toEqual({ status: 'completed' })
    await completed
    expect(submitted).toEqual([])
  })

  it.each(['throw', 'reject'] as const)(
    'submits failed tool results even when the error observer fails by %s',
    async (observerFailure) => {
      for (const failure of ['handler', 'invalid result']) {
        let posts = 0
        const { client, submitted } = api((_, body) =>
          failure === 'invalid result' && posts++ === 0
            ? Response.json({ code: 'validation_failed', error: failure }, { status: 400 })
            : accepted(body ?? found),
        )
        const onError = vi.fn(() => {
          if (observerFailure === 'throw') throw new Error('logger broke')
          return Promise.reject(new Error('logger broke'))
        })
        const worker = createCustomToolWorker({
          client,
          path,
          onError,
          tools: {
            lookup_customer: () =>
              failure === 'handler' ? Promise.reject(new Error(failure)) : Promise.resolve(found),
          },
        })
        expect(await worker.handle(ready)).toEqual({ status: 'completed' })
        expect(submitted).toEqual(failure === 'handler' ? [failed()] : [found, failed()])
        expect(onError).toHaveBeenCalledOnce()
        await worker.shutdown()
      }
    },
  )

  it.each(['throw', 'reject'] as const)(
    'finishes unrelated result delivery during shutdown when the error observer fails by %s',
    async (observerFailure) => {
      const nextID = `tcl_${'b'.repeat(26)}`
      let finishSubmit: (() => void) | undefined
      const releases = new Map<string, (result: SubmitToolCallResultRequest) => void>()
      const { client, submitted } = api((request, body) => {
        if (request.url.includes(nextID))
          return Response.json({ code: 'forbidden', error: 'forbidden' }, { status: 403 })
        return new Promise<Response>((resolve, reject) => {
          finishSubmit = () => {
            resolve(accepted(body ?? found))
          }
          request.signal.addEventListener('abort', () => {
            reject(new Error('aborted'))
          })
        })
      })
      const onError = vi.fn(() => {
        if (observerFailure === 'throw') throw new Error('logger broke')
        return Promise.reject(new Error('logger broke'))
      })
      const worker = createCustomToolWorker({
        client,
        path,
        onError,
        tools: {
          lookup_customer: (call) =>
            new Promise((resolve) => {
              releases.set(call.toolCallID, resolve)
            }),
        },
      })
      const first = worker.handle(ready)
      const second = worker.handle({ ...ready, tool_call_id: nextID })
      await vi.waitFor(() => {
        expect(releases.size).toBe(2)
      })
      let shutdownFinished = false
      const stopped = worker.shutdown().then(() => {
        shutdownFinished = true
      })
      for (const resolve of releases.values()) resolve(found)
      expect(await second).toMatchObject({ status: 'failed', error: { status: 403 } })
      expect(shutdownFinished).toBe(false)
      expect(finishSubmit).toBeDefined()
      finishSubmit?.()
      expect(await first).toEqual({ status: 'completed' })
      await stopped
      expect(submitted).toEqual([found, found])
      expect(onError).toHaveBeenCalledOnce()
    },
  )
})
