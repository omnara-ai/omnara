import { afterEach, describe, expect, it, vi } from 'vitest'

import { createCustomToolWorker } from './custom-tool-worker'
import {
  accepted,
  api,
  callPath,
  found,
  path,
  ready,
  toolCall,
  toolCallID,
  toolPage,
} from './custom-tool-worker-test-support'
import type {
  CustomToolHandler,
  CustomToolObservation,
  CustomToolOutcome,
} from './custom-tool-worker-types'

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

const nextID = `tcl_${'b'.repeat(26)}`

describe('custom tool worker concurrency', () => {
  it('lets concurrent checks reach later calls while preserving the shared capacity', async () => {
    const { client, requests } = api((request, body) =>
      body === undefined ? toolPage(request, [toolCall(), toolCall(nextID)]) : accepted(body),
    )
    const handler = vi.fn<CustomToolHandler>(({ toolCallID: id }) =>
      Promise.resolve(id === toolCallID ? { outcome: 'deferred' } : found),
    )
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
    const results = await Promise.all([worker.check({ limit: 2 }), worker.check({ limit: 2 })])
    for (const result of results) expect(result).toMatchObject({ nextCursor: null, blocked: false })
    expect(handler.mock.calls.filter(([call]) => call.toolCallID === toolCallID)).toHaveLength(2)
    expect(handler.mock.calls.filter(([call]) => call.toolCallID === nextID)).toHaveLength(1)
    expect(requests.filter((request) => request === `GET ${callPath}`)).toHaveLength(0)
    for (const event of observations) {
      if (event.stage === 'capacity')
        expect(event.running + event.retained + event.reserved).toBeLessThanOrEqual(1)
    }
    await worker.shutdown()
  })

  it('releases a reservation when its observer initiates shutdown before the read', async () => {
    const { client, requests } = api(() => {
      throw new Error('must not fetch')
    })
    let shutdown: Promise<void> | undefined
    const observations: CustomToolObservation[] = []
    const worker = createCustomToolWorker({
      client,
      path,
      concurrency: 1,
      tools: { lookup_customer: () => Promise.resolve(found) },
      onError: vi.fn(),
      onObserve: (event) => {
        observations.push(event)
        if (event.stage === 'capacity' && event.reserved > 0 && shutdown === undefined)
          shutdown = worker.shutdown()
      },
    })
    await expect(worker.check()).rejects.toThrow()
    await shutdown
    expect(requests).toHaveLength(0)
    expect(observations.at(-1)).toEqual({ stage: 'capacity', running: 0, retained: 0, reserved: 0 })
  })

  it('registers a task before an admission observer delivers its completion', async () => {
    const { client, requests } = api(() => {
      throw new Error('must not fetch')
    })
    const handler = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    let completed: Promise<CustomToolOutcome> | undefined
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: handler },
      onError: vi.fn(),
      onObserve: (event) => {
        if (event.stage === 'admission' && event.outcome === 'started')
          completed = worker.handle({ ...ready, state: 'completed' })
      },
    })
    expect(await worker.handle(ready)).toEqual({ status: 'completed' })
    await completed
    expect(handler).not.toHaveBeenCalled()
    expect(requests).toHaveLength(0)
    await worker.shutdown()
  })

  it('delivers a fallback when the error callback awaits shutdown of its own task', async () => {
    const { client, submitted } = api((_, body) => accepted(body ?? found))
    let shutdown: Promise<void> | undefined
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: () => Promise.reject(new Error('handler failed')) },
      onError: async () => {
        shutdown = worker.shutdown()
        await shutdown
      },
    })
    expect(await worker.handle(ready)).toEqual({ status: 'completed' })
    await shutdown
    expect(submitted).toHaveLength(1)
    expect(submitted[0]?.outcome).toBe('failed')
  })

  it('excludes the interval between recovery passes from the pass duration', async () => {
    vi.useFakeTimers({
      toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'],
    })
    vi.spyOn(performance, 'now').mockImplementation(() => Date.now())
    const { client } = api(() => Response.json({ data: [], next_cursor: null }))
    const durations: number[] = []
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: () => Promise.resolve(found) },
      onError: vi.fn(),
      onObserve: (event) => {
        if (event.stage === 'recovery' && event.outcome === 'complete')
          durations.push(event.durationMs)
      },
    })
    const running = worker.run()
    await vi.advanceTimersByTimeAsync(0)
    expect(durations).toEqual([0])
    await vi.advanceTimersByTimeAsync(60_000)
    expect(durations).toEqual([0, 0])
    await worker.shutdown()
    await running
  })
})
