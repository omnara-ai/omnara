import { afterEach, describe, expect, it, vi } from 'vitest'

import { trackAbortListeners } from './abort-test-support'
import { createCustomToolWorker } from './custom-tool-worker'
import {
  accepted,
  api,
  found,
  input,
  path,
  profileID,
  ready,
  toolCall,
  toolCallID,
  toolPage,
} from './custom-tool-worker-test-support'
import type { CustomToolHandler, CustomToolObservation } from './custom-tool-worker-types'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((accept) => {
    resolve = accept
  })
  return { promise, resolve }
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('finite custom tool handling', () => {
  it('joins concurrent notifications and reads authoritative input before executing', async () => {
    const pending = deferred<typeof found>()
    const handler = vi.fn<CustomToolHandler>(() => pending.promise)
    const { client, requests, submitted } = api(
      (_request, body) => accepted(body ?? found),
      () => Response.json({ ...toolCall(), input: { authoritative: true } }),
    )
    const worker = createCustomToolWorker({ client, path, tools: { lookup_customer: handler } })
    const first = worker.handle(ready)
    const duplicate = worker.handle(ready)
    await vi.waitFor(() => {
      expect(handler).toHaveBeenCalledOnce()
    })
    expect(handler.mock.calls[0]?.[0]).toMatchObject({
      agentID: ready.agent_id,
      toolCallID,
      input: { authoritative: true },
    })
    pending.resolve(found)
    await expect(first).resolves.toEqual({ status: 'completed' })
    await expect(duplicate).resolves.toEqual({ status: 'completed' })
    expect(submitted).toEqual([found])
    expect(requests).toHaveLength(2)
  })

  it('filters project, organization, profile, state and unregistered names before I/O', async () => {
    const { client, requests } = api(() => {
      throw new Error('unexpected request')
    })
    const handler = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      agentProfileID: profileID,
      tools: { lookup_customer: handler },
    })
    for (const update of [
      { ...ready, org_id: 'another' },
      { ...ready, project_id: 'another' },
      { ...ready, agent_profile_id: null },
      { ...ready, name: 'another' },
      { ...ready, name: undefined, input: undefined },
      { ...ready, state: 'running' as const },
    ])
      expect(await worker.handle(update)).toEqual({ status: 'ignored' })
    expect(requests).toEqual([])
    expect(handler).not.toHaveBeenCalled()
  })

  it('does not execute a delayed ready event when the current call is canceled', async () => {
    const handler = vi.fn()
    const { client, submitted } = api(
      () => {
        throw new Error('unexpected submit')
      },
      () => Response.json({ ...toolCall(), state: 'completed', outcome: 'canceled' }),
    )
    const worker = createCustomToolWorker({ client, path, tools: { lookup_customer: handler } })
    await expect(worker.handle(ready)).resolves.toEqual({ status: 'completed' })
    expect(handler).not.toHaveBeenCalled()
    expect(submitted).toEqual([])
  })

  it('cancels only the duplicate wait and cleans up caller listeners', async () => {
    const pending = deferred<typeof found>()
    const started = deferred<AbortSignal>()
    const { client } = api((_request, body) => accepted(body ?? found))
    const worker = createCustomToolWorker({
      client,
      path,
      tools: {
        lookup_customer: async ({ signal }) => {
          started.resolve(signal)
          return pending.promise
        },
      },
    })
    const owner = new AbortController()
    const ownerListeners = trackAbortListeners(owner.signal)
    const first = worker.handle(ready, { signal: owner.signal })
    const handlerSignal = await started.promise
    const duplicate = new AbortController()
    const duplicateListeners = trackAbortListeners(duplicate.signal)
    const other = worker.handle(ready, { signal: duplicate.signal })
    const reason = new Error('duplicate stopped')
    const rejected = expect(other).rejects.toBe(reason)
    duplicate.abort(reason)
    await rejected
    expect(handlerSignal.aborted).toBe(false)
    pending.resolve(found)
    await first
    expect(ownerListeners.size).toBe(0)
    expect(duplicateListeners.size).toBe(0)
  })

  it('uses completed updates to cancel a cooperative handler without submitting', async () => {
    const started = deferred<undefined>()
    const { client, submitted } = api(() => {
      throw new Error('unexpected submit')
    })
    const worker = createCustomToolWorker({
      client,
      path,
      tools: {
        lookup_customer: ({ signal }) =>
          new Promise((_, reject) => {
            signal.addEventListener(
              'abort',
              () => {
                reject(new Error('call completed'))
              },
              { once: true },
            )
            started.resolve(undefined)
          }),
      },
    })
    const first = worker.handle(ready)
    await started.promise
    await expect(worker.handle({ ...ready, state: 'completed' })).resolves.toEqual({
      status: 'completed',
    })
    await expect(first).resolves.toEqual({ status: 'completed' })
    expect(submitted).toEqual([])
  })

  it('starts all concurrently submitted calls and awaits each result', async () => {
    const held = deferred<typeof found>()
    const handler = vi.fn(() => held.promise)
    const { client, submitted } = api((_request, body) => accepted(body ?? found))
    const worker = createCustomToolWorker({ client, path, tools: { lookup_customer: handler } })
    const calls = Array.from({ length: 12 }, (_, i) =>
      worker.handle({ ...ready, tool_call_id: `tcl_${String.fromCharCode(97 + i).repeat(26)}` }),
    )
    try {
      await vi.waitFor(
        () => {
          expect(handler).toHaveBeenCalledTimes(12)
        },
        { timeout: 1000 },
      )
      expect(submitted).toEqual([])
    } finally {
      held.resolve(found)
      await Promise.all(calls)
    }
    expect(await Promise.all(calls)).toEqual(
      Array.from({ length: 12 }, () => ({ status: 'completed' })),
    )
    expect(submitted).toHaveLength(12)
  })

  it('uses the generated tool-name contract at construction', () => {
    const { client } = api(() => Response.json({}))
    for (const name of ['', 'a'.repeat(65)]) {
      expect(() =>
        createCustomToolWorker({ client, path, tools: { [name]: () => Promise.resolve(found) } }),
      ).toThrow(/Invalid custom tool name/)
    }
    expect(() =>
      createCustomToolWorker({
        client,
        path,
        tools: { ['a'.repeat(64)]: () => Promise.resolve(found) },
      }),
    ).not.toThrow()
  })
})

describe('finite recovery pages', () => {
  it('awaits exactly one page and preserves a cursor past deferred work', async () => {
    const calls = Array.from({ length: 4 }, (_, i) =>
      toolCall(`tcl_${String.fromCharCode(97 + i).repeat(26)}`),
    )
    const { client, requests, submitted } = api((request) => toolPage(request, calls))
    const handler = vi.fn<CustomToolHandler>(() =>
      Promise.resolve({ outcome: 'deferred' as const }),
    )
    const observations: CustomToolObservation[] = []
    const worker = createCustomToolWorker({
      client,
      path,
      onObserve: (event) => {
        observations.push(event)
      },
      agentProfileID: profileID,
      tools: { lookup_customer: handler },
    })
    const first = await worker.check({ limit: 2 })
    expect(first.results.map(({ call }) => call.toolCallID)).toEqual(
      calls.slice(0, 2).map(({ id }) => id),
    )
    expect(first.results.map(({ outcome }) => outcome)).toEqual([
      { status: 'deferred' },
      { status: 'deferred' },
    ])
    expect(first.nextCursor).not.toBeNull()
    const second = await worker.check({ cursor: first.nextCursor ?? undefined, limit: 2 })
    expect(second.results.map(({ call }) => call.toolCallID)).toEqual(
      calls.slice(2).map(({ id }) => id),
    )
    expect(second.nextCursor).toBeNull()
    expect(handler).toHaveBeenCalledTimes(4)
    const params = new URLSearchParams(requests[0]?.split('?')[1])
    expect(params.get('agent_profile_id')).toBe(profileID)
    expect(params.getAll('names')).toEqual(['lookup_customer'])
    expect(params.get('state')).toBe('ready')
    expect(params.get('type')).toBe('custom')
    await worker.check({ limit: 2 })
    expect(requests).toHaveLength(3)
    expect(
      requests.every((request) => request.startsWith('GET ') && request.includes('/tool-calls?')),
    ).toBe(true)
    expect(submitted).toEqual([])
    expect(observations.some((event) => event.stage === 'readiness')).toBe(false)
    expect(handler.mock.calls[0]?.[0]).toMatchObject({ input: calls[0]?.input })
  })

  it('supports more than 100 registered tools without an invalid names filter', async () => {
    const { client, requests } = api(() =>
      Response.json({
        data: [{ ...toolCall(), name: 'unregistered' }, toolCall()],
        next_cursor: 'later',
      }),
    )
    const tools = Object.fromEntries(
      Array.from({ length: 101 }, (_, i) => [
        `tool_${i}`,
        () => Promise.resolve({ outcome: 'deferred' as const }),
      ]),
    )
    tools.lookup_customer = () => Promise.resolve({ outcome: 'deferred' as const })
    const page = await createCustomToolWorker({ client, path, tools }).check()
    expect(new URLSearchParams(requests[0]?.split('?')[1]).has('names')).toBe(false)
    expect(page.results).toHaveLength(1)
    expect(page.nextCursor).toBe('later')
  })

  it.each(['completed', 'deferred'] as const)(
    'handles a delayed page after a local %s outcome',
    async (outcome) => {
      const list = deferred<Response>()
      let completed = false
      const handler = vi.fn<CustomToolHandler>(() =>
        Promise.resolve(outcome === 'completed' ? found : { outcome: 'deferred' }),
      )
      const { client, requests } = api(
        (request, body) => {
          if (request.method === 'GET') return list.promise
          completed = true
          return accepted(body ?? found)
        },
        () =>
          Response.json({
            ...toolCall(),
            state: completed ? 'completed' : 'ready',
            outcome: completed ? 'succeeded' : undefined,
          }),
      )
      const worker = createCustomToolWorker({ client, path, tools: { lookup_customer: handler } })
      const check = worker.check()
      await worker.handle(ready)
      list.resolve(Response.json({ data: [toolCall()], next_cursor: null }))
      expect((await check).results[0]?.outcome.status).toBe(outcome)
      expect(handler).toHaveBeenCalledTimes(outcome === 'completed' ? 1 : 2)
      expect(requests).toHaveLength(outcome === 'completed' ? 3 : 2)
    },
  )

  it('protects overlapping list reads from completion notifications even when one fails', async () => {
    const lists = [deferred<Response>(), deferred<Response>(), deferred<Response>()]
    let listed = 0
    const { client, requests } = api(() => {
      const list = lists[listed++]
      if (!list) throw new Error('unexpected request')
      return list.promise
    })
    const handler = vi.fn(() => Promise.resolve(found))
    const worker = createCustomToolWorker({ client, path, tools: { lookup_customer: handler } })
    const checks = [worker.check(), worker.check(), worker.check()]
    const failed = expect(checks[1]).rejects.toThrow()
    await vi.waitFor(() => {
      expect(listed).toBe(3)
    })
    await worker.handle({ ...ready, state: 'completed' })
    lists[1]?.resolve(Response.json({ code: 'not_found', error: 'unavailable' }, { status: 404 }))
    await failed
    for (const index of [0, 2]) {
      lists[index]?.resolve(Response.json({ data: [toolCall()], next_cursor: null }))
      expect((await checks[index])?.results[0]?.outcome.status).toBe('completed')
    }
    expect(handler).not.toHaveBeenCalled()
    expect(requests).toHaveLength(3)
  })

  it('joins the same running handler across overlapping discovery pages', async () => {
    const held = deferred<typeof found>()
    const handler = vi.fn(() => held.promise)
    const { client, requests, submitted } = api((request, body) =>
      request.method === 'GET'
        ? Response.json({ data: [toolCall()], next_cursor: null })
        : accepted(body ?? found),
    )
    const worker = createCustomToolWorker({ client, path, tools: { lookup_customer: handler } })
    const first = worker.check()
    await vi.waitFor(() => {
      expect(handler).toHaveBeenCalledOnce()
    })
    const second = worker.check()
    await vi.waitFor(() => {
      expect(requests).toHaveLength(2)
    })
    held.resolve(found)
    const pages = await Promise.all([first, second])
    expect(pages.map((page) => page.results[0]?.outcome.status)).toEqual(['completed', 'completed'])
    expect(handler).toHaveBeenCalledOnce()
    expect(submitted).toEqual([found])
    expect(requests).toHaveLength(3)
  })

  it.each([0, 101, 1.5])('rejects invalid page limit %s before I/O', async (limit) => {
    const { client, requests } = api(() => Response.json({}))
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: () => Promise.resolve(found) },
    })
    await expect(worker.check({ limit })).rejects.toThrow()
    expect(requests).toEqual([])
  })

  it('returns immediately for no registered tools', async () => {
    const { client, requests } = api(() => Response.json({}))
    expect(
      await createCustomToolWorker({ client, path, tools: {} }).check({ cursor: 'old' }),
    ).toEqual({ results: [], nextCursor: null })
    expect(requests).toEqual([])
  })

  it('reports handler failure separately from accepted delivery and isolates diagnostics', async () => {
    const observations: CustomToolObservation[] = []
    const { client, submitted } = api((_request, body) => accepted(body ?? found))
    const worker = createCustomToolWorker({
      client,
      path,
      tools: {
        lookup_customer: () => Promise.reject(new Error('private detail')),
      },
      onError: () => Promise.reject(new Error('diagnostics failed')),
      onObserve: (value) => {
        observations.push(value)
        throw new Error('observer failed')
      },
    })
    await expect(worker.handle(ready)).resolves.toEqual({ status: 'completed' })
    expect(submitted[0]?.outcome).toBe('failed')
    expect(JSON.stringify(submitted)).not.toContain('private detail')
    expect(observations).toContainEqual(
      expect.objectContaining({ stage: 'handler', outcome: 'failed' }),
    )
    expect(observations).toContainEqual(
      expect.objectContaining({ stage: 'submission', outcome: 'accepted' }),
    )
    expect(JSON.stringify(observations)).not.toContain(toolCallID)
    expect(JSON.stringify(observations)).not.toContain(input.email)
  })
})

it('loads complete input when a named ready notification omits large input', async () => {
  const largeInput = { text: 'a'.repeat(64 * 1024 + 1) }
  const handler = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
  const { client, requests, submitted } = api(
    (_request, body) => accepted(body ?? found),
    () => Response.json({ ...toolCall(), input: largeInput }),
  )
  const worker = createCustomToolWorker({ client, path, tools: { lookup_customer: handler } })
  await expect(worker.handle({ ...ready, input: undefined })).resolves.toEqual({
    status: 'completed',
  })
  expect(handler).toHaveBeenCalledOnce()
  expect(handler.mock.calls[0]?.[0]).toMatchObject({ name: ready.name, input: largeInput })
  expect(requests.filter((request) => request.startsWith('GET '))).toHaveLength(1)
  expect(submitted).toEqual([found])
})

it('lets a completed-notification caller cancel its wait while the owning handler settles', async () => {
  const held = deferred<typeof found>()
  const started = deferred<AbortSignal>()
  const { client, submitted } = api((_request, body) => accepted(body ?? found))
  const worker = createCustomToolWorker({
    client,
    path,
    tools: {
      lookup_customer: ({ signal }) => {
        started.resolve(signal)
        return held.promise
      },
    },
  })
  const owner = worker.handle(ready)
  const handlerSignal = await started.promise
  const waiter = new AbortController()
  const listeners = trackAbortListeners(waiter.signal)
  const completion = worker.handle({ ...ready, state: 'completed' }, { signal: waiter.signal })
  const reason = new Error('completed-notification caller stopped waiting')
  const rejected = expect(completion).rejects.toBe(reason)
  waiter.abort(reason)
  await rejected
  expect(handlerSignal.aborted).toBe(true)
  expect(listeners.size).toBe(0)
  held.resolve(found)
  await expect(owner).resolves.toEqual({ status: 'completed' })
  expect(submitted).toEqual([])
})

it('aborts an in-flight result POST when a completed notification arrives', async () => {
  const started = deferred<AbortSignal>()
  const response = deferred<Response>()
  const { client, submitted } = api((request) => {
    started.resolve(request.signal)
    return response.promise
  })
  const handler = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
  const worker = createCustomToolWorker({ client, path, tools: { lookup_customer: handler } })
  const owner = worker.handle(ready)
  const requestSignal = await started.promise
  await expect(worker.handle({ ...ready, state: 'completed' })).resolves.toEqual({
    status: 'completed',
  })
  await expect(owner).resolves.toEqual({ status: 'completed' })
  expect(requestSignal.aborted).toBe(true)
  expect(handler).toHaveBeenCalledOnce()
  expect(submitted).toEqual([found])
  response.resolve(accepted(found))
})
