import { afterEach, describe, expect, it, vi } from 'vitest'

import { createOmnaraClient } from './client'
import { createCustomToolWorker, type CustomToolHandler } from './custom-tool-worker'
import {
  accepted,
  agentID,
  agentPath,
  api,
  callPath,
  failed,
  found,
  input,
  path,
  profileID,
  projectPath,
  ready,
  routing,
  toolCall,
  toolCallID,
} from './custom-tool-worker-test-support'
import type { SubmitToolCallResultRequest } from './generated/types.gen'

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('createCustomToolWorker', () => {
  it('runs each ready custom update once and submits its result', async () => {
    const { client, requests, submitted } = api((_, body) => accepted(body ?? found))
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: lookup },
      onError,
    })

    void worker.handle(ready)
    void worker.handle(ready)
    void worker.handle({ ...ready, state: 'running' })
    void worker.handle({ ...ready, name: 'other_tool' })
    void worker.handle({ ...routing, tool_call_id: toolCallID, agent_id: agentID, state: 'ready' })

    await vi.waitFor(() => {
      expect(submitted).toEqual([found])
    })
    expect(lookup).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({ agentID, toolCallID, name: 'lookup_customer', input }),
    )
    expect(requests).toEqual([`GET ${callPath}`, `POST ${callPath}/result`])
    expect(onError).not.toHaveBeenCalled()
  })

  it('filters organization, project, and profile before making any requests', async () => {
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const { client, requests } = api((_, body) => accepted(body ?? found))
    const worker = createCustomToolWorker({
      client,
      path,
      agentProfileID: profileID,
      tools: { lookup_customer: lookup },
      onError: vi.fn(),
    })
    for (const update of [
      { ...ready, org_id: `org_${'b'.repeat(26)}` },
      { ...ready, project_id: `proj_${'b'.repeat(26)}` },
      { ...ready, agent_profile_id: `aprf_${'b'.repeat(26)}` },
      { ...ready, agent_profile_id: null },
    ])
      expect(await worker.handle(update)).toEqual({ status: 'ignored' })
    expect(requests).toEqual([])
    expect(await worker.handle(ready)).toEqual({ status: 'completed' })
    expect(requests).toEqual([`GET ${callPath}`, `POST ${callPath}/result`])
    expect(lookup).toHaveBeenCalledOnce()
  })

  it('submits handler errors and rejected results as failures', async () => {
    const rankID = `tcl_${'b'.repeat(26)}`
    const sizeID = `tcl_${'c'.repeat(26)}`
    const { client, submitted } = api(
      (request, body = found) => {
        if (body.outcome === 'failed') return accepted(body)
        return request.url.includes(sizeID)
          ? Response.json(
              { error: 'request too large', code: 'request_too_large' },
              { status: 413 },
            )
          : Response.json({ error: 'invalid result', code: 'validation_failed' }, { status: 400 })
      },
      (request) =>
        Response.json({
          ...toolCall(),
          name: request.url.includes(rankID)
            ? 'rank_customer'
            : request.url.includes(sizeID)
              ? 'size_customer'
              : 'lookup_customer',
        }),
    )
    const crashed = new Error('crm unavailable')
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      tools: {
        lookup_customer: () => Promise.reject(crashed),
        rank_customer: () => Promise.resolve(found),
        size_customer: () => Promise.resolve(found),
      },
      onError,
    })

    void worker.handle(ready)
    void worker.handle({ ...ready, tool_call_id: rankID, name: 'rank_customer' })
    void worker.handle({ ...ready, tool_call_id: sizeID, name: 'size_customer' })

    await vi.waitFor(() => {
      expect(submitted).toHaveLength(5)
    })
    expect(submitted).toEqual(
      expect.arrayContaining([
        failed('crm unavailable'),
        failed('invalid result'),
        failed('request too large'),
      ]),
    )
    expect(onError).toHaveBeenCalledWith(crashed, {
      agentID,
      toolCallID,
      name: 'lookup_customer',
    })
    expect(onError).toHaveBeenCalledWith(expect.objectContaining({ status: 400 }), {
      agentID,
      toolCallID: rankID,
      name: 'rank_customer',
    })
    expect(onError).toHaveBeenCalledWith(expect.objectContaining({ status: 413 }), {
      agentID,
      toolCallID: sizeID,
      name: 'size_customer',
    })
  })

  it('retries network errors and unavailable submissions, then accepts a result submitted elsewhere', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const responses = [
      new TypeError('fetch failed'),
      Response.json({ error: 'unavailable', code: 'service_unavailable' }, { status: 503 }),
      Response.json(
        { error: 'tool call already completed', code: 'tool_call_already_completed' },
        { status: 409 },
      ),
    ]
    const { client, requests } = api(() => {
      const next = responses.shift() ?? new Response(null, { status: 500 })
      if (next instanceof Error) throw next
      return next
    })
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: () => Promise.resolve(found) },
      onError,
    })

    void worker.handle(ready)

    await vi.waitFor(() => {
      expect(requests).toHaveLength(4)
    })
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(onError).not.toHaveBeenCalled()
  })

  it('reports a request that never reached the API instead of retrying it', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>()
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client: createOmnaraClient({ baseUrl: 'api.example.com/v1', fetch }),
      path,
      tools: { lookup_customer: () => Promise.resolve(found) },
      onError,
    })

    void worker.handle(ready)

    await vi.waitFor(() => {
      expect(onError).toHaveBeenCalledWith(expect.any(TypeError), {
        agentID,
        toolCallID,
        name: 'lookup_customer',
      })
    })
    expect(fetch).not.toHaveBeenCalled()
  })

  it('does not run tools once aborted', async () => {
    const { client, requests } = api(() => new Response(null, { status: 500 }))
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: lookup },
      signal: AbortSignal.abort(),
      onError,
    })

    void worker.handle(ready)

    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(lookup).not.toHaveBeenCalled()
    expect(requests).toEqual([])
    expect(onError).not.toHaveBeenCalled()
  })

  it('resends a failed result at the next regular check, even while busy', async () => {
    vi.useFakeTimers({
      toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'],
    })
    const controller = new AbortController()
    const others = Array.from(
      { length: 10 },
      (_, index) => `tcl_${String.fromCharCode(98 + index).repeat(26)}`,
    )
    const finished = new Set<string>()
    let rejections = 1
    const { client } = api((request, body) => {
      if (body === undefined) {
        const ids = [toolCallID, ...others].filter((id) => !finished.has(id))
        return Response.json({ data: ids.map((id) => toolCall(id)), next_cursor: null })
      }
      const id = /tool-calls\/([^/]+)\/result/.exec(request.url)?.[1] ?? ''
      if (id === toolCallID && rejections-- > 0) {
        return Response.json({ error: 'forbidden', code: 'forbidden' }, { status: 403 })
      }
      finished.add(id)
      return accepted(body)
    })
    const lookup = vi.fn<CustomToolHandler>((call) =>
      call.toolCallID === toolCallID
        ? Promise.resolve(found)
        : new Promise((resolve) => {
            setTimeout(() => {
              resolve(found)
            }, 10_000)
          }),
    )
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: lookup },
      concurrency: 2,
      signal: controller.signal,
      onError,
    })
    const stopped = worker.run()
    await vi.advanceTimersByTimeAsync(50_000)
    expect(finished.has(toolCallID)).toBe(false)
    await vi.advanceTimersByTimeAsync(20_000)
    expect(finished.has(toolCallID)).toBe(true)
    expect(finished.size).toBeLessThan(others.length)
    expect(lookup.mock.calls.filter(([call]) => call.toolCallID === toolCallID)).toHaveLength(1)

    controller.abort()
    await vi.advanceTimersByTimeAsync(10_000)
    await stopped
    expect(onError).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ status: 403 }), {
      agentID,
      toolCallID,
      name: 'lookup_customer',
    })
  })

  it('distinguishes busy, ignored, completed, failed and stopped outcomes', async () => {
    const rankID = `tcl_${'b'.repeat(26)}`
    const deniedID = `tcl_${'c'.repeat(26)}`
    const release: ((result: SubmitToolCallResultRequest) => void)[] = []
    const { client } = api((request, body = found) =>
      request.url.includes(deniedID)
        ? Response.json({ error: 'forbidden', code: 'forbidden' }, { status: 403 })
        : accepted(body),
    )
    const worker = createCustomToolWorker({
      client,
      path,
      tools: {
        lookup_customer: (call) =>
          call.toolCallID === toolCallID
            ? new Promise((resolve) => {
                release.push(resolve)
              })
            : Promise.resolve(found),
      },
      concurrency: 1,
      onError: vi.fn(),
    })

    const first = worker.handle(ready)
    await vi.waitFor(() => {
      expect(release).toHaveLength(1)
    })
    expect(await worker.handle({ ...ready, tool_call_id: rankID })).toMatchObject({
      status: 'busy',
    })
    expect(await worker.handle({ ...ready, tool_call_id: rankID, name: 'other_tool' })).toEqual({
      status: 'ignored',
    })
    release[0]?.(found)
    expect(await first).toMatchObject({ status: 'completed' })
    expect(await worker.handle({ ...ready, tool_call_id: deniedID })).toMatchObject({
      status: 'failed',
      error: { status: 403 },
    })
    await worker.shutdown()
    expect(await worker.handle(ready)).toEqual({ status: 'stopped' })
  })

  it('waits for the whole page and returns an explicit cursor for the next check', async () => {
    const release: ((result: SubmitToolCallResultRequest) => void)[] = []
    const { client } = api((request, body) => {
      if (body !== undefined) return accepted(body)
      return new URL(request.url).searchParams.has('cursor')
        ? Response.json({ error: 'forbidden', code: 'forbidden' }, { status: 403 })
        : Response.json({ data: [toolCall()], next_cursor: 'next' })
    })
    const worker = createCustomToolWorker({
      client,
      path,
      tools: {
        lookup_customer: () =>
          new Promise((resolve) => {
            release.push(resolve)
          }),
      },
      onError: vi.fn(),
    })

    const checked = worker.check()
    let done = false
    void checked.then(() => {
      done = true
    })
    await vi.waitFor(() => {
      expect(release).toHaveLength(1)
    })
    expect(done).toBe(false)
    release[0]?.(found)
    expect(await checked).toMatchObject({
      nextCursor: 'next',
      results: [{ outcome: { status: 'completed' } }],
    })
    await expect(worker.check({ cursor: 'next' })).rejects.toMatchObject({ status: 403 })
  })

  it('stops taking work while results cannot be submitted, then resends them', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    const rankID = `tcl_${'b'.repeat(26)}`
    const finished = new Set<string>()
    let rejectSubmits = true
    const { client } = api((request, body) => {
      if (body === undefined) {
        const ids = [toolCallID, rankID].filter((id) => !finished.has(id))
        return Response.json({ data: ids.map((id) => toolCall(id)), next_cursor: null })
      }
      if (rejectSubmits) {
        return Response.json({ error: 'forbidden', code: 'forbidden' }, { status: 403 })
      }
      finished.add(/tool-calls\/([^/]+)\/result/.exec(request.url)?.[1] ?? '')
      return accepted(body)
    })
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: lookup },
      concurrency: 1,
      onError: vi.fn(),
    })
    const ran = () => lookup.mock.calls.map(([call]) => call.toolCallID)

    await worker.check()
    await worker.check()
    expect(ran()).toEqual([toolCallID])

    rejectSubmits = false
    finished.add(toolCallID)
    vi.setSystemTime(Date.now() + 60_000)
    await worker.check()
    await worker.check()
    expect(ran()).toEqual([toolCallID, rankID])
    expect(finished).toEqual(new Set([toolCallID, rankID]))
  })

  it('lets handle() resend only its own unsubmitted result, without rerunning the tool', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    const rankID = `tcl_${'b'.repeat(26)}`
    let rejectSubmits = true
    const { client, requests } = api((_, body = found) =>
      rejectSubmits
        ? Response.json({ error: 'not found', code: 'not_found' }, { status: 404 })
        : accepted(body),
    )
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: () => Promise.resolve(found) },
      onError,
    })

    expect(await worker.handle(ready)).toMatchObject({ status: 'failed' })
    expect(onError).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ status: 404 }), {
      agentID,
      toolCallID,
      name: 'lookup_customer',
    })
    expect(await worker.handle({ ...ready, tool_call_id: rankID })).toMatchObject({
      status: 'failed',
    })
    expect(await worker.handle(ready)).toMatchObject({ status: 'failed' })
    rejectSubmits = false
    vi.setSystemTime(Date.now() + 60_000)
    const before = requests.length
    expect(await worker.handle(ready)).toMatchObject({ status: 'completed' })
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(requests.slice(before)).toEqual([`POST ${callPath}/result`])
  })

  it('does not rerun a completed call from a stale list or a late update', async () => {
    let submits = 0
    let list: ((response: Response) => void) | undefined
    const { client } = api(
      (_, body) => {
        if (body === undefined) {
          return new Promise((resolve) => {
            list = resolve
          })
        }
        return ++submits === 1
          ? Response.json({ error: 'forbidden', code: 'forbidden' }, { status: 403 })
          : accepted(body)
      },
      () => Response.json({ ...toolCall(), state: submits > 1 ? 'completed' : 'ready' }),
    )
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: lookup },
      onError: vi.fn(),
    })

    expect(await worker.handle(ready)).toMatchObject({ status: 'failed' })
    const checked = worker.check()
    await vi.waitFor(() => {
      expect(submits).toBe(2)
    })
    await new Promise((resolve) => setTimeout(resolve, 0))
    list?.(Response.json({ data: [toolCall()], next_cursor: null }))
    await checked
    expect(await worker.handle(ready)).toMatchObject({ status: 'completed' })
    expect(lookup).toHaveBeenCalledTimes(1)
  })

  it('frees the slot of a parked result when its call completes', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    const rankID = `tcl_${'b'.repeat(26)}`
    const { client, requests } = api((request, body = found) =>
      request.url.includes(toolCallID)
        ? Response.json({ error: 'forbidden', code: 'forbidden' }, { status: 403 })
        : accepted(body),
    )
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: () => Promise.resolve(found) },
      concurrency: 1,
      onError: vi.fn(),
    })

    expect(await worker.handle(ready)).toMatchObject({ status: 'failed' })
    vi.setSystemTime(Date.now() + 60_000)
    const before = requests.length
    expect(
      await worker.handle({
        ...routing,
        tool_call_id: toolCallID,
        agent_id: agentID,
        state: 'completed',
      }),
    ).toEqual({ status: 'completed' })
    expect(requests).toHaveLength(before)
    expect(await worker.handle({ ...ready, tool_call_id: rankID })).toMatchObject({
      status: 'completed',
    })
  })

  it('stops a submission when its call completes, even during shutdown', async () => {
    const controller = new AbortController()
    const rankID = `tcl_${'b'.repeat(26)}`
    const { client, requests } = api(
      (request) =>
        new Promise((_, reject) => {
          request.signal.addEventListener('abort', () => {
            reject(new Error('aborted'))
          })
        }),
    )
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: () => Promise.resolve(found) },
      concurrency: 1,
      signal: controller.signal,
      onError: vi.fn(),
    })

    const first = worker.handle(ready)
    await vi.waitFor(() => {
      expect(requests).toContain(`POST ${callPath}/result`)
    })
    void worker.handle({
      ...routing,
      tool_call_id: toolCallID,
      agent_id: agentID,
      state: 'completed',
    })
    expect(await first).toMatchObject({ status: 'completed' })
    const next = worker.handle({ ...ready, tool_call_id: rankID })
    await vi.waitFor(() => {
      expect(requests).toContain(`POST ${agentPath}/tool-calls/${rankID}/result`)
    })
    controller.abort()
    void worker.handle({ ...routing, tool_call_id: rankID, agent_id: agentID, state: 'completed' })
    expect(await next).toMatchObject({ status: 'completed' })
  })

  it('rejects a concurrency that is not a positive integer', () => {
    for (const concurrency of [0, -1, 1.5, Number.NaN]) {
      expect(() =>
        createCustomToolWorker({
          client: createOmnaraClient(),
          path,
          tools: {},
          concurrency,
          onError: vi.fn(),
        }),
      ).toThrow(RangeError)
    }
  })

  it('sweeps ready calls for its tools until aborted', async () => {
    const controller = new AbortController()
    const { client, requests, submitted } = api((request, body) => {
      if (body !== undefined) {
        controller.abort()
        return accepted(body)
      }
      return Response.json(
        new URL(request.url).searchParams.has('cursor')
          ? { data: [toolCall()], next_cursor: null }
          : { data: [], next_cursor: 'next' },
      )
    })
    const worker = createCustomToolWorker({
      client,
      path,
      agentProfileID: profileID,
      tools: { lookup_customer: () => Promise.resolve(found) },
      signal: controller.signal,
      onError: vi.fn(),
    })

    await worker.run()

    const list = `GET ${projectPath}/tool-calls?state=ready&type=custom&names=lookup_customer&agent_profile_id=${profileID}`
    expect(requests).toEqual([
      list,
      `${list}&cursor=next`,
      `GET ${callPath}`,
      `POST ${callPath}/result`,
    ])
    expect(submitted).toEqual([found])
  })
  it('processes every call in a recovery page even with concurrency one', async () => {
    const calls = ['a', 'b', 'c', 'd'].map((letter) => toolCall(`tcl_${letter.repeat(26)}`))
    let active = 0
    let maximum = 0
    const { client, requests, submitted } = api((_, body) =>
      body === undefined
        ? Response.json({ data: calls, next_cursor: 'next-page' })
        : accepted(body),
    )
    const worker = createCustomToolWorker({
      client,
      path,
      concurrency: 1,
      onError: vi.fn(),
      tools: {
        lookup_customer: async () => {
          maximum = Math.max(maximum, ++active)
          await Promise.resolve()
          active--
          return found
        },
      },
    })
    const checked = await worker.check({ limit: 4 })
    expect(maximum).toBe(1)
    expect(submitted).toHaveLength(4)
    expect(checked.results.map(({ call, outcome }) => [call.toolCallID, outcome.status])).toEqual(
      calls.map((call) => [call.id, 'completed']),
    )
    expect(checked.nextCursor).toBe('next-page')
    expect(requests[0]).toContain('limit=4')
  })

  it('scans forward past a failed admission without repeatedly reading the oldest page', async () => {
    const nextID = `tcl_${'b'.repeat(26)}`
    const cursors: (string | null)[] = []
    const { client, requests, submitted } = api(
      (request, body) => {
        if (body !== undefined) return accepted(body)
        const cursor = new URL(request.url).searchParams.get('cursor')
        cursors.push(cursor)
        return Response.json(
          cursor === null
            ? { data: [toolCall()], next_cursor: 'next-page' }
            : { data: [toolCall(nextID)], next_cursor: null },
        )
      },
      (request) =>
        request.url.endsWith(toolCallID)
          ? Response.json({ code: 'forbidden', error: 'forbidden' }, { status: 403 })
          : Response.json(toolCall(nextID)),
    )
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      concurrency: 1,
      onError,
      tools: { lookup_customer: () => Promise.resolve(found) },
    })
    const done = worker.run()
    await vi.waitFor(() => {
      expect(submitted).toEqual([found])
    })
    await worker.shutdown()
    await done
    expect(cursors).toEqual([null, 'next-page'])
    expect(requests.filter((request) => request === `GET ${callPath}`)).toHaveLength(1)
    expect(onError).toHaveBeenCalledOnce()
  })

  it('reports retained-result failure and capacity exhaustion independently', async () => {
    const nextID = `tcl_${'b'.repeat(26)}`
    const { client } = api((_, body) =>
      body === undefined
        ? Response.json({ data: [toolCall(), toolCall(nextID)], next_cursor: null })
        : Response.json({ code: 'forbidden', error: 'forbidden' }, { status: 403 }),
    )
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const worker = createCustomToolWorker({
      client,
      path,
      concurrency: 1,
      onError: vi.fn(),
      tools: { lookup_customer: lookup },
    })
    const checked = await worker.check()
    expect(checked.results.map(({ outcome }) => outcome.status)).toEqual(['failed', 'busy'])
    expect(lookup).toHaveBeenCalledOnce()
    await worker.check()
    expect(lookup).toHaveBeenCalledOnce()
    await worker.shutdown()
  })

  it.each([403, 404])(
    'reports admission %s and permits an explicit retry without polling',
    async (status) => {
      let allowed = false
      const { client } = api(
        (_, body) => accepted(body ?? found),
        () =>
          allowed
            ? Response.json(toolCall())
            : Response.json(
                { code: status === 404 ? 'not_found' : 'forbidden', error: 'unavailable' },
                { status },
              ),
      )
      const onError = vi.fn()
      const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
      const worker = createCustomToolWorker({
        client,
        path,
        onError,
        tools: { lookup_customer: lookup },
      })
      expect(await worker.handle(ready)).toMatchObject({ status: 'failed', error: { status } })
      expect(lookup).not.toHaveBeenCalled()
      allowed = true
      expect(await worker.handle(ready)).toEqual({ status: 'completed' })
      expect(onError).toHaveBeenCalledOnce()
    },
  )

  it('retains the replacement failed result when its submission needs retrying', async () => {
    let count = 0
    const { client, submitted } = api((_, body) => {
      if (++count === 1)
        return Response.json({ code: 'validation_failed', error: 'bad result' }, { status: 400 })
      if (count === 2)
        return Response.json({ code: 'forbidden', error: 'forbidden' }, { status: 403 })
      return accepted(body ?? found)
    })
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const worker = createCustomToolWorker({
      client,
      path,
      onError: vi.fn(),
      tools: { lookup_customer: lookup },
    })
    expect(await worker.handle(ready)).toMatchObject({ status: 'failed' })
    expect(await worker.handle(ready)).toEqual({ status: 'completed' })
    expect(lookup).toHaveBeenCalledOnce()
    expect(submitted).toEqual([found, failed('bad result'), failed('bad result')])
  })
})
