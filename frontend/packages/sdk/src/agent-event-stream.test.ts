import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  AgentEventStreamError,
  type AgentEventStreamFrame,
  openAgentEventStream,
} from './agent-event-stream'
import { createOmnaraClient } from './client'
import type { ModelOutputEvent } from './generated/types.gen'

const path = { orgID: 'org', projectID: 'project', agentID: 'agent' }
const idSuffix = 'a'.repeat(26)
const toolUpdate = { tool_call_id: `tcl_${idSuffix}`, state: 'running' as const }

function durableEvent(sequence: number, eventKind = 'model_output'): ModelOutputEvent {
  return {
    id: `evt_${idSuffix}`,
    org_id: `org_${idSuffix}`,
    project_id: `proj_${idSuffix}`,
    agent_id: `agt_${idSuffix}`,
    turn_id: `trn_${idSuffix}`,
    turn_sequence: 1,
    is_opening_event: false,
    sequence,
    event_kind: eventKind as 'model_output',
    model_call_context_id: `mcc_${idSuffix}`,
    stop_reason: 'end_turn',
    content_blocks: [],
    created_at: '2026-08-25T00:00:00Z',
  }
}

function sse(...frames: unknown[]): Response {
  return new Response(frames.map((frame) => `data: ${JSON.stringify(frame)}\n\n`).join(''), {
    status: 200,
    headers: { 'Content-Type': 'text/event-stream; charset=utf-8' },
  })
}

function controlledSse(initial: string) {
  const cancel = vi.fn()
  let streamController: ReadableStreamDefaultController<Uint8Array> | undefined
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      streamController = controller
      controller.enqueue(new TextEncoder().encode(initial))
    },
    cancel,
  })
  return {
    cancel,
    close: () => streamController?.close(),
    enqueue: (chunk: string) => streamController?.enqueue(new TextEncoder().encode(chunk)),
    response: new Response(body, {
      status: 200,
      headers: { 'Content-Type': 'text/event-stream' },
    }),
  }
}

function clientWithFetch(fetch: typeof globalThis.fetch) {
  const client = createOmnaraClient({ baseUrl: 'https://api.example.test' })
  client.setConfig({ fetch })
  return client
}

function scriptedClient(...results: (Response | Error)[]) {
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(() => {
    const result = results.shift()
    if (result == null) return Promise.resolve(new Response(null, { status: 401 }))
    if (result instanceof Error) return Promise.reject(result)
    return Promise.resolve(result)
  })
  return { client: clientWithFetch(fetch), fetch }
}

async function collectUntilError(
  stream: AsyncGenerator<AgentEventStreamFrame>,
): Promise<{ frames: AgentEventStreamFrame[]; error: AgentEventStreamError }> {
  const frames: AgentEventStreamFrame[] = []
  try {
    for await (const frame of stream) frames.push(frame)
  } catch (error) {
    if (error instanceof AgentEventStreamError) return { frames, error }
    throw error
  }
  throw new Error('stream unexpectedly completed')
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('openAgentEventStream', () => {
  it('is lazy, validates frames, and reconnects after clean EOF', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const { client, fetch } = scriptedClient(sse(toolUpdate), new Response(null, { status: 401 }))
    const states: unknown[] = []

    const stream = openAgentEventStream({
      client,
      path,
      onConnectionStateChange: (state) => states.push(state),
    })
    expect(fetch).not.toHaveBeenCalled()

    const result = await collectUntilError(stream)

    expect(result.frames).toEqual([toolUpdate])
    expect(result.error).toMatchObject({ kind: 'http', status: 401 })
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(states).toMatchObject([
      { state: 'connected', reconnected: false },
      { state: 'reconnecting', attempt: 1, delayMs: 0 },
    ])
  })

  it('retries transport failures and preserves the caller boundary before a durable frame', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const { client, fetch } = scriptedClient(
      new TypeError('connection reset'),
      sse(toolUpdate),
      new Response(null, { status: 401 }),
    )

    const result = await collectUntilError(
      openAgentEventStream({
        client,
        path,
        query: { after_sequence: 20 },
        headers: { 'Last-Event-ID': 10 },
      }),
    )

    expect(result.frames).toEqual([toolUpdate])
    const requests = fetch.mock.calls.map(([request]) => request as Request)
    expect(requests).toHaveLength(3)
    expect(requests.map((request) => request.headers.get('Last-Event-ID'))).toEqual([
      '10',
      '10',
      '10',
    ])
    expect(
      requests.map((request) => new URL(request.url).searchParams.get('after_sequence')),
    ).toEqual(['20', '20', '20'])
  })

  it('resumes from the last yielded durable sequence and filters replays', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const { client, fetch } = scriptedClient(
      sse(durableEvent(11)),
      sse(durableEvent(11), durableEvent(12)),
      new Response(null, { status: 401 }),
    )

    const result = await collectUntilError(
      openAgentEventStream({ client, path, query: { after_sequence: 10 } }),
    )

    expect(
      result.frames.map((frame) => ('sequence' in frame ? frame.sequence : undefined)),
    ).toEqual([11, 12])
    const requests = fetch.mock.calls.map(([request]) => request as Request)
    expect(requests.map((request) => request.headers.get('Last-Event-ID'))).toEqual([
      null,
      '11',
      '12',
    ])
  })

  it('acknowledges a durable cursor only when the consumer requests the next frame', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const { client, fetch } = scriptedClient(
      sse(durableEvent(11)),
      new Response(null, { status: 401 }),
    )
    const stream = openAgentEventStream({ client, path, query: { after_sequence: 10 } })

    await expect(stream.next()).resolves.toMatchObject({ done: false, value: { sequence: 11 } })
    await Promise.resolve()
    expect(fetch).toHaveBeenCalledTimes(1)

    await expect(stream.next()).rejects.toMatchObject({ kind: 'http', status: 401 })
    expect(fetch).toHaveBeenCalledTimes(2)
    expect((fetch.mock.calls[1]?.[0] as Request).headers.get('Last-Event-ID')).toBe('11')
  })

  it('uses an unknown durable event kind as a forward-compatible cursor', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const { client, fetch } = scriptedClient(
      sse(durableEvent(7, 'future_event')),
      new Response(null, { status: 401 }),
    )

    const result = await collectUntilError(openAgentEventStream({ client, path }))

    expect(result.frames).toHaveLength(1)
    expect((fetch.mock.calls[1]?.[0] as Request).headers.get('Last-Event-ID')).toBe('7')
  })

  it('does not trust an SSE id attached to an ephemeral frame', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const first = new Response(
      `id: 99\nevent: tool_call_update\ndata: ${JSON.stringify(toolUpdate)}\n\n`,
      { status: 200, headers: { 'Content-Type': 'text/event-stream' } },
    )
    const { client, fetch } = scriptedClient(first, new Response(null, { status: 401 }))

    await collectUntilError(
      openAgentEventStream({
        client,
        path,
        query: { after_sequence: 4 },
        headers: { 'Last-Event-ID': 3 },
      }),
    )

    expect((fetch.mock.calls[1]?.[0] as Request).headers.get('Last-Event-ID')).toBe('3')
  })

  it('retries service_unavailable envelopes but throws other API errors', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const { client, fetch } = scriptedClient(
      sse({ error: 'temporarily unavailable', code: 'service_unavailable' }),
      sse({ error: 'bad request', code: 'invalid_request' }),
    )

    const result = await collectUntilError(openAgentEventStream({ client, path }))

    expect(result.frames).toEqual([])
    expect(result.error).toMatchObject({ kind: 'api', code: 'invalid_request' })
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it.each([408, 429, 500, 503])('retries HTTP %s', async (status) => {
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const { client, fetch } = scriptedClient(
      new Response(null, { status }),
      new Response(null, { status: 401 }),
    )

    const result = await collectUntilError(openAgentEventStream({ client, path }))

    expect(result.error).toMatchObject({ kind: 'http', status: 401 })
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('honors Retry-After before reconnecting', async () => {
    vi.useFakeTimers()
    let reconnecting!: () => void
    const sawReconnecting = new Promise<void>((resolve) => {
      reconnecting = resolve
    })
    const { client, fetch } = scriptedClient(
      new Response(null, { status: 503, headers: { 'Retry-After': '2' } }),
      new Response(null, { status: 401 }),
    )
    const result = collectUntilError(
      openAgentEventStream({
        client,
        path,
        onConnectionStateChange(state) {
          if (state.state === 'reconnecting') reconnecting()
        },
      }),
    )
    await sawReconnecting
    expect(fetch).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(1_999)
    expect(fetch).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)

    await expect(result).resolves.toMatchObject({ error: { status: 401 } })
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('parses an HTTP-date Retry-After value', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-25T12:00:00Z'))
    const controller = new AbortController()
    let reconnectDelay: number | undefined
    const { client } = scriptedClient(
      new Response(null, {
        status: 503,
        headers: { 'Retry-After': 'Tue, 25 Aug 2026 12:00:03 GMT' },
      }),
    )
    const next = openAgentEventStream({
      client,
      path,
      signal: controller.signal,
      onConnectionStateChange(state) {
        if (state.state !== 'reconnecting') return
        reconnectDelay = state.delayMs
        controller.abort()
      },
    }).next()

    await expect(next).resolves.toEqual({ done: true, value: undefined })
    expect(reconnectDelay).toBe(3_000)
  })

  it('caps Retry-After at 60 seconds', async () => {
    const controller = new AbortController()
    let reconnectDelay: number | undefined
    const { client } = scriptedClient(
      new Response(null, { status: 503, headers: { 'Retry-After': '600' } }),
    )
    const next = openAgentEventStream({
      client,
      path,
      signal: controller.signal,
      onConnectionStateChange(state) {
        if (state.state !== 'reconnecting') return
        reconnectDelay = state.delayMs
        controller.abort()
      },
    }).next()

    await expect(next).resolves.toEqual({ done: true, value: undefined })
    expect(reconnectDelay).toBe(60_000)
  })

  it('honors Retry-After zero as an immediate retry', async () => {
    const controller = new AbortController()
    let reconnectDelay: number | undefined
    const { client } = scriptedClient(
      new Response(null, { status: 503, headers: { 'Retry-After': '0' } }),
    )
    const next = openAgentEventStream({
      client,
      path,
      signal: controller.signal,
      onConnectionStateChange(state) {
        if (state.state !== 'reconnecting') return
        reconnectDelay = state.delayMs
        controller.abort()
      },
    }).next()

    await expect(next).resolves.toEqual({ done: true, value: undefined })
    expect(reconnectDelay).toBe(0)
  })

  it('grows backoff, resets after a yielded frame, and never resets on an error envelope', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    const resetScript = scriptedClient(
      new Response(null, { status: 503 }),
      new Response(null, { status: 503 }),
      sse(toolUpdate),
      new Response(null, { status: 401 }),
    )
    const resetStates: { state: string; delayMs?: number }[] = []
    await collectUntilError(
      openAgentEventStream({
        client: resetScript.client,
        path,
        onConnectionStateChange: (state) => {
          resetStates.push(state)
        },
      }),
    )
    expect(
      resetStates.flatMap((state) => (state.state === 'reconnecting' ? [state.delayMs] : [])),
    ).toEqual([500, 1_000, 500])

    const errorScript = scriptedClient(
      new Response(null, { status: 503 }),
      sse({ error: 'still unavailable', code: 'service_unavailable' }),
      new Response(null, { status: 401 }),
    )
    const errorStates: { state: string; delayMs?: number }[] = []
    await collectUntilError(
      openAgentEventStream({
        client: errorScript.client,
        path,
        onConnectionStateChange: (state) => {
          errorStates.push(state)
        },
      }),
    )
    expect(
      errorStates.flatMap((state) => (state.state === 'reconnecting' ? [state.delayMs] : [])),
    ).toEqual([500, 1_000])
  })

  it('resets backoff after a connection remains stable for 10 seconds', async () => {
    vi.useFakeTimers()
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    const active = controlledSse(': ok\n\n')
    const controller = new AbortController()
    const { client } = scriptedClient(new Response(null, { status: 503 }), active.response)
    const delays: number[] = []
    const next = openAgentEventStream({
      client,
      path,
      signal: controller.signal,
      onConnectionStateChange(state) {
        if (state.state !== 'reconnecting') return
        delays.push(state.delayMs)
        if (delays.length === 2) controller.abort()
      },
    }).next()

    await vi.advanceTimersByTimeAsync(500)
    await vi.advanceTimersByTimeAsync(10_000)
    active.close()
    await vi.advanceTimersByTimeAsync(0)

    await expect(next).resolves.toEqual({ done: true, value: undefined })
    expect(delays).toEqual([500, 500])
  })

  it('caps exponential backoff at 30 seconds', async () => {
    vi.useFakeTimers()
    vi.spyOn(Math, 'random').mockReturnValue(1)
    const controller = new AbortController()
    const { client } = scriptedClient(
      ...Array.from({ length: 7 }, () => new Response(null, { status: 503 })),
    )
    const delays: number[] = []
    const next = openAgentEventStream({
      client,
      path,
      signal: controller.signal,
      onConnectionStateChange(state) {
        if (state.state !== 'reconnecting') return
        delays.push(state.delayMs)
        if (delays.length === 7) controller.abort()
      },
    }).next()

    await vi.runAllTimersAsync()
    await expect(next).resolves.toEqual({ done: true, value: undefined })
    expect(delays).toEqual([1_000, 2_000, 4_000, 8_000, 16_000, 30_000, 30_000])
  })

  it.each([204, 302, 400, 401, 403])('treats HTTP %s as terminal', async (status) => {
    const { client, fetch } = scriptedClient(new Response(null, { status }))

    const result = await collectUntilError(openAgentEventStream({ client, path }))

    expect(result.error).toMatchObject({ kind: 'http', status })
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('fails terminally on malformed data and an invalid media type', async () => {
    const malformed = scriptedClient(
      new Response('data: not JSON\n\n', {
        status: 200,
        headers: { 'Content-Type': 'text/event-stream' },
      }),
    )
    const wrongMedia = scriptedClient(
      new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }),
    )

    await expect(
      collectUntilError(openAgentEventStream({ client: malformed.client, path })),
    ).resolves.toMatchObject({ error: { kind: 'contract' } })
    await expect(
      collectUntilError(openAgentEventStream({ client: wrongMedia.client, path })),
    ).resolves.toMatchObject({ error: { kind: 'contract' } })
    expect(malformed.fetch).toHaveBeenCalledTimes(1)
    expect(wrongMedia.fetch).toHaveBeenCalledTimes(1)
  })

  it('preserves forward-compatible fields on a valid known frame', async () => {
    const frame = { ...toolUpdate, future_optional_field: 'preserved' }
    const { client } = scriptedClient(sse(frame), new Response(null, { status: 401 }))

    const result = await collectUntilError(openAgentEventStream({ client, path }))

    expect(result.frames[0]).toEqual(frame)
  })

  it('fails terminally when a successful response has no body', async () => {
    const { client, fetch } = scriptedClient(
      new Response(null, { status: 200, headers: { 'Content-Type': 'text/event-stream' } }),
    )

    const result = await collectUntilError(openAgentEventStream({ client, path }))

    expect(result.error).toMatchObject({ kind: 'contract' })
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('runs request interceptors on every physical connection', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const { client, fetch } = scriptedClient(
      new TypeError('offline'),
      new Response(null, { status: 401 }),
    )
    const interceptor = vi.fn((request: Request) => {
      request.headers.set('X-Test-Auth', 'present')
      return request
    })
    client.interceptors.request.use(interceptor)

    await collectUntilError(openAgentEventStream({ client, path }))

    expect(interceptor).toHaveBeenCalledTimes(2)
    expect(
      fetch.mock.calls.map(([request]) => (request as Request).headers.get('X-Test-Auth')),
    ).toEqual(['present', 'present'])
  })

  it('does not retry request setup and interceptor failures', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>()
    const client = clientWithFetch(fetch)
    client.interceptors.request.use(() => {
      throw new Error('missing credentials')
    })

    const result = await collectUntilError(openAgentEventStream({ client, path }))

    expect(result.error).toMatchObject({ kind: 'client' })
    expect(fetch).not.toHaveBeenCalled()
  })

  it('ends normally when the caller aborts during backoff', async () => {
    vi.useFakeTimers()
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    const controller = new AbortController()
    const { client, fetch } = scriptedClient(new TypeError('offline'))
    const stream = openAgentEventStream({ client, path, signal: controller.signal })
    const done = stream.next()
    await vi.waitFor(() => {
      expect(fetch).toHaveBeenCalledTimes(1)
    })

    controller.abort()

    await expect(done).resolves.toEqual({ done: true, value: undefined })
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('ends normally and cancels the physical body when aborted during an active read', async () => {
    const active = controlledSse(': ok\n\n')
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(active.response)
    const controller = new AbortController()
    const stream = openAgentEventStream({
      client: clientWithFetch(fetch),
      path,
      signal: controller.signal,
    })
    const next = stream.next()
    await vi.waitFor(() => {
      expect(fetch).toHaveBeenCalledTimes(1)
    })

    controller.abort()

    await expect(next).resolves.toEqual({ done: true, value: undefined })
    expect(active.cancel).toHaveBeenCalledTimes(1)
  })

  it('does not report a connection when fetch resolves after caller cancellation', async () => {
    const active = controlledSse(': ok\n\n')
    let resolveFetch!: (response: Response) => void
    let fetchStarted!: () => void
    const started = new Promise<void>((resolve) => {
      fetchStarted = resolve
    })
    const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(
      () =>
        new Promise<Response>((resolve) => {
          resolveFetch = resolve
          fetchStarted()
        }),
    )
    const controller = new AbortController()
    const states: unknown[] = []
    const next = openAgentEventStream({
      client: clientWithFetch(fetch),
      path,
      signal: controller.signal,
      onConnectionStateChange: (state) => states.push(state),
    }).next()
    await started

    controller.abort()
    resolveFetch(active.response)

    await expect(next).resolves.toEqual({ done: true, value: undefined })
    expect(states).toEqual([])
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(active.cancel).toHaveBeenCalledTimes(1)
  })

  it('cancels before reading when the connected callback aborts synchronously', async () => {
    const active = controlledSse(': ok\n\n')
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(active.response)
    const controller = new AbortController()
    const states: unknown[] = []
    const next = openAgentEventStream({
      client: clientWithFetch(fetch),
      path,
      signal: controller.signal,
      onConnectionStateChange(state) {
        states.push(state)
        controller.abort()
      },
    }).next()

    await expect(next).resolves.toEqual({ done: true, value: undefined })
    expect(states).toMatchObject([{ state: 'connected', reconnected: false }])
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(active.cancel).toHaveBeenCalledTimes(1)
  })

  it('cancels the physical body without reconnecting when the consumer returns', async () => {
    const active = controlledSse(`data: ${JSON.stringify(toolUpdate)}\n\n`)
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(active.response)
    const stream = openAgentEventStream({ client: clientWithFetch(fetch), path })

    await expect(stream.next()).resolves.toEqual({ done: false, value: toolUpdate })
    await stream.return(undefined)

    expect(active.cancel).toHaveBeenCalledTimes(1)
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('does not run the active-read watchdog while the consumer holds a yielded frame', async () => {
    vi.useFakeTimers()
    const active = controlledSse(`data: ${JSON.stringify(toolUpdate)}\n\n`)
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(active.response)
    const controller = new AbortController()
    const stream = openAgentEventStream({
      client: clientWithFetch(fetch),
      path,
      signal: controller.signal,
    })

    await expect(stream.next()).resolves.toEqual({ done: false, value: toolUpdate })
    await vi.advanceTimersByTimeAsync(36_000)
    expect(fetch).toHaveBeenCalledTimes(1)
    controller.abort()
    await stream.return(undefined)
  })

  it('reconnects when an active read receives no frames for 35 seconds', async () => {
    vi.useFakeTimers()
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const active = controlledSse(': ok\n\n')
    const { client, fetch } = scriptedClient(active.response, new Response(null, { status: 401 }))
    let connected!: () => void
    const sawConnected = new Promise<void>((resolve) => {
      connected = resolve
    })
    const next = openAgentEventStream({
      client,
      path,
      onConnectionStateChange(state) {
        if (state.state === 'connected') connected()
      },
    }).next()
    const rejected = expect(next).rejects.toMatchObject({ kind: 'http', status: 401 })
    await sawConnected
    await vi.advanceTimersByTimeAsync(0)
    expect(fetch).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(35_000)
    await vi.runOnlyPendingTimersAsync()

    await rejected
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(active.cancel).toHaveBeenCalledTimes(1)
  })

  it('keeps an active read alive when periodic SSE comments arrive', async () => {
    vi.useFakeTimers()
    const active = controlledSse(': ok\n\n')
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(active.response)
    const controller = new AbortController()
    const next = openAgentEventStream({
      client: clientWithFetch(fetch),
      path,
      signal: controller.signal,
    }).next()
    await vi.advanceTimersByTimeAsync(0)

    for (let heartbeat = 0; heartbeat < 3; heartbeat += 1) {
      await vi.advanceTimersByTimeAsync(30_000)
      active.enqueue(': heartbeat\n\n')
      await vi.advanceTimersByTimeAsync(0)
    }

    expect(fetch).toHaveBeenCalledTimes(1)
    controller.abort()
    await expect(next).resolves.toEqual({ done: true, value: undefined })
  })

  it('treats a connected callback exception as a terminal client error', async () => {
    const active = controlledSse('')
    const { client, fetch } = scriptedClient(active.response)

    const result = await collectUntilError(
      openAgentEventStream({
        client,
        path,
        onConnectionStateChange: () => {
          throw new Error('callback failed')
        },
      }),
    )

    expect(result.error).toMatchObject({ kind: 'client' })
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(active.cancel).toHaveBeenCalledTimes(1)
  })

  it('emits connected, reconnecting, then reconnected for a recovered connection', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const controller = new AbortController()
    const { client } = scriptedClient(sse(toolUpdate), controlledSse(': ok\n\n').response)
    const states: { state: string; reconnected?: boolean }[] = []
    const stream = openAgentEventStream({
      client,
      path,
      signal: controller.signal,
      onConnectionStateChange(state) {
        states.push(state)
        if (state.state === 'connected' && state.reconnected) controller.abort()
      },
    })

    expect(await stream.next()).toEqual({ done: false, value: toolUpdate })
    await expect(stream.next()).resolves.toEqual({ done: true, value: undefined })
    expect(states.map((state) => state.state)).toEqual(['connected', 'reconnecting', 'connected'])
    expect(states.at(-1)).toMatchObject({ reconnected: true })
  })
})
