import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  createCustomToolWorker,
  type CustomToolHandler,
  type CustomToolObservation,
} from './custom-tool-worker'
import {
  accepted,
  api,
  callPath,
  failed,
  found,
  path,
  ready,
  toolCall,
  toolCallID,
  toolPage,
} from './custom-tool-worker-test-support'
import type { SubmitToolCallResultRequest } from './generated/types.gen'

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

const nextID = `tcl_${'b'.repeat(26)}`

describe('custom tool admission contract', () => {
  it('does not execute an already canceled call from a signed ready snapshot with input', async () => {
    const { client, requests } = api(
      () => {
        throw new Error('must not submit')
      },
      () => Response.json({ ...toolCall(), state: 'completed', outcome: 'canceled' }),
    )
    const handler = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: handler },
      onError: vi.fn(),
    })
    expect(await worker.handle(ready)).toEqual({ status: 'completed' })
    expect(handler).not.toHaveBeenCalled()
    expect(requests).toEqual([`GET ${callPath}`])
    await worker.shutdown()
  })

  it('uses the authoritative call input when starting from a notification', async () => {
    const currentInput = { email: 'current@example.com' }
    const { client } = api(
      (_, body) => accepted(body ?? found),
      () => Response.json({ ...toolCall(), input: currentInput }),
    )
    const handler = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: handler },
      onError: vi.fn(),
    })
    await worker.handle(ready)
    expect(handler).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({ input: currentInput }),
    )
    await worker.shutdown()
  })

  it('reserves the read capacity against simultaneous notification admission', async () => {
    let releaseList: ((response: Response) => void) | undefined
    const { client, requests } = api((_, body) =>
      body === undefined
        ? new Promise((resolve) => {
            releaseList = resolve
          })
        : accepted(body),
    )
    const handler = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const observations: CustomToolObservation[] = []
    const worker = createCustomToolWorker({
      client,
      path,
      concurrency: 1,
      tools: { lookup_customer: handler },
      onError: vi.fn(),
      onObserve: (event) => {
        observations.push(event)
      },
    })
    const checked = worker.check({ limit: 1 })
    await vi.waitFor(() => {
      expect(releaseList).toBeDefined()
    })
    expect(await worker.handle({ ...ready, tool_call_id: nextID })).toEqual({ status: 'busy' })
    expect(requests).toHaveLength(1)
    releaseList?.(Response.json({ data: [toolCall()], next_cursor: null }))
    expect(await checked).toMatchObject({
      blocked: false,
      results: [{ outcome: { status: 'completed' } }],
    })
    expect(handler).toHaveBeenCalledOnce()
    for (const event of observations) {
      if (event.stage === 'capacity')
        expect(event.running + event.retained + event.reserved).toBeLessThanOrEqual(1)
    }
    await worker.shutdown()
  })

  it('waits before listing, so calls canceled while capacity is occupied never enter the page', async () => {
    let release: ((result: SubmitToolCallResultRequest) => void) | undefined
    let pending = [toolCall(nextID)]
    let lists = 0
    const { client } = api((request, body) => {
      if (body !== undefined) return accepted(body)
      lists++
      return toolPage(request, pending)
    })
    const handler = vi.fn<CustomToolHandler>(
      () =>
        new Promise((resolve) => {
          release = resolve
        }),
    )
    const worker = createCustomToolWorker({
      client,
      path,
      concurrency: 1,
      tools: { lookup_customer: handler },
      onError: vi.fn(),
    })
    const handled = worker.handle(ready)
    await vi.waitFor(() => {
      expect(release).toBeDefined()
    })
    const checked = worker.check()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(lists).toBe(0)
    pending = []
    release?.(found)
    await handled
    expect(await checked).toMatchObject({ results: [], nextCursor: null, blocked: false })
    expect(lists).toBe(1)
    expect(handler).toHaveBeenCalledOnce()
    await worker.shutdown()
  })

  it('returns a blocked checkpoint without discovering or skipping calls when retained results fill capacity', async () => {
    const { client, requests } = api(() =>
      Response.json({ code: 'not_found', error: 'not found' }, { status: 404 }),
    )
    const handler = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const worker = createCustomToolWorker({
      client,
      path,
      concurrency: 1,
      tools: { lookup_customer: handler },
      onError: vi.fn(),
    })
    await worker.handle(ready)
    const checked = await worker.check({ cursor: 'saved-cursor' })
    expect(checked).toMatchObject({
      blocked: true,
      nextCursor: 'saved-cursor',
      results: [{ outcome: { status: 'failed' } }],
    })
    expect(requests.filter((request) => request.includes('/tool-calls?'))).toEqual([])
    expect(handler).toHaveBeenCalledOnce()
    await worker.shutdown()
  })

  it('preserves a failed page cursor and reaches later work without restarting old deferred calls', async () => {
    vi.useFakeTimers({
      toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'],
    })
    let fail = true
    const cursors: (string | null)[] = []
    const { client, requests, submitted } = api((request, body) => {
      if (body !== undefined) return accepted(body)
      const cursor = new URL(request.url).searchParams.get('cursor')
      cursors.push(cursor)
      if (cursor !== null && fail) {
        fail = false
        return Response.json({ code: 'forbidden', error: 'unavailable' }, { status: 403 })
      }
      return toolPage(request, [toolCall(), toolCall(nextID)])
    })
    const handler = vi.fn<CustomToolHandler>(({ toolCallID: id }) =>
      Promise.resolve(id === toolCallID ? { outcome: 'deferred' } : found),
    )
    const worker = createCustomToolWorker({
      client,
      path,
      concurrency: 1,
      tools: { lookup_customer: handler },
      onError: vi.fn(),
    })
    const running = worker.run()
    await vi.advanceTimersByTimeAsync(60_001)
    expect(cursors).toEqual([null, `after:${toolCallID}`, `after:${toolCallID}`])
    expect(handler.mock.calls.map(([call]) => call.toolCallID)).toEqual([toolCallID, nextID])
    expect(submitted).toEqual([found])
    expect(requests.filter((request) => request === `GET ${callPath}`)).toEqual([])
    await worker.shutdown()
    await running
  })

  it('retains an unknown 400 instead of rewriting a valid result', async () => {
    let allowed = false
    const { client, submitted } = api((_, body) =>
      allowed
        ? accepted(body ?? found)
        : Response.json({ code: 'new_error', error: 'retry later' }, { status: 400 }),
    )
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: () => Promise.resolve(found) },
      onError: vi.fn(),
    })
    expect(await worker.handle(ready)).toMatchObject({ status: 'failed' })
    expect(submitted).toEqual([found])
    allowed = true
    await worker.handle(ready)
    expect(submitted).toEqual([found, found])
    await worker.shutdown()
  })

  it('remembers fallback identity across a transient rejection and never replaces it repeatedly', async () => {
    let attempts = 0
    const { client, submitted } = api(() =>
      ++attempts === 2
        ? Response.json({ code: 'not_found', error: 'unavailable' }, { status: 404 })
        : Response.json({ code: 'invalid_request', error: 'invalid result' }, { status: 400 }),
    )
    const handler = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: handler },
      onError: vi.fn(),
    })
    await worker.handle(ready)
    expect(submitted).toEqual([found, failed()])
    await worker.handle(ready)
    await worker.handle(ready)
    expect(submitted).toEqual([found, failed(), failed()])
    expect(handler).toHaveBeenCalledOnce()
    await worker.shutdown()
  })

  it('separates handler failure from successful delivery and isolates observer failures', async () => {
    const events: CustomToolObservation[] = []
    const { client } = api((_, body) => accepted(body ?? found))
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: () => Promise.reject(new Error('private detail')) },
      onError: vi.fn(),
      onObserve: (event) => {
        events.push(event)
        if (events.length % 2) throw new Error('observer failed')
        return Promise.reject(new Error('observer failed asynchronously'))
      },
    })
    expect(await worker.handle(ready)).toEqual({ status: 'completed' })
    expect(events).toContainEqual(expect.objectContaining({ stage: 'handler', outcome: 'failed' }))
    expect(events).toContainEqual(
      expect.objectContaining({ stage: 'submission', outcome: 'accepted' }),
    )
    expect(JSON.stringify(events)).not.toContain(toolCallID)
    expect(JSON.stringify(events)).not.toContain('private detail')
    await worker.shutdown()
  })
})
