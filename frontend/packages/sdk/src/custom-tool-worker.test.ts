import { afterEach, describe, expect, it, vi } from 'vitest'

import { createOmnaraClient } from './client'
import { createCustomToolWorker, type CustomToolHandler } from './custom-tool-worker'
import type { SubmitToolCallResultRequest, ToolCallUpdate } from './generated/types.gen'
import { zSubmitToolCallResultRequest } from './generated/zod.gen'

const idSuffix = 'a'.repeat(26)
const agentID = `agt_${idSuffix}`
const toolCallID = `tcl_${idSuffix}`
const path = { orgID: `org_${idSuffix}`, projectID: `proj_${idSuffix}` }
const projectPath = `/v1/orgs/${path.orgID}/projects/${path.projectID}`
const agentPath = `${projectPath}/agents/${agentID}`
const callPath = `${agentPath}/tool-calls/${toolCallID}`
const profileID = `aprf_${idSuffix}`
const input = { email: 'ada@example.com' }
const ready: ToolCallUpdate = {
  tool_call_id: toolCallID,
  agent_id: agentID,
  state: 'ready',
  name: 'lookup_customer',
  input,
}
const found: SubmitToolCallResultRequest = {
  outcome: 'succeeded',
  content_blocks: [{ type: 'text', text: 'found' }],
}

function failed(error: string): SubmitToolCallResultRequest {
  return { outcome: 'failed', content_blocks: [{ type: 'structured_data', value: { error } }] }
}

function toolCall(id = toolCallID) {
  return {
    id,
    agent_id: agentID,
    turn_id: `trn_${idSuffix}`,
    provider_call_id: 'call_1',
    name: 'lookup_customer',
    input,
    type: 'custom',
    state: 'ready',
    created_at: '2026-11-04T00:00:00Z',
  }
}

function agentRecord(): Response {
  return Response.json({
    agent: {
      id: agentID,
      org_id: path.orgID,
      project_id: path.projectID,
      agent_profile_id: profileID,
      state: 'active',
      name: 'support',
      created_at: '2026-11-04T00:00:00Z',
      updated_at: '2026-11-04T00:00:00Z',
    },
    machine_ids: [],
    mcp_connections: [],
  })
}

function accepted(body: SubmitToolCallResultRequest): Response {
  return Response.json(
    {
      tool_call: { ...toolCall(), state: 'completed', outcome: body.outcome },
      tool_result: {
        ...body,
        event_id: `evt_${idSuffix}`,
        agent_id: agentID,
        tool_call_id: toolCallID,
        created_at: '2026-11-04T00:00:00Z',
      },
    },
    { status: 201 },
  )
}

function api(
  route: (request: Request, body?: SubmitToolCallResultRequest) => Response | Promise<Response>,
  call = () => Response.json(toolCall()),
) {
  const requests: string[] = []
  const submitted: SubmitToolCallResultRequest[] = []
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const request = new Request(input, init)
    request.signal.throwIfAborted()
    const url = new URL(request.url)
    requests.push(`${request.method} ${url.pathname}${url.search}`)
    if (url.pathname === agentPath) return agentRecord()
    if (/\/tool-calls\/tcl_[a-z2-7]{26}$/.test(url.pathname)) return call()
    if (request.method !== 'POST') return route(request)
    const body = zSubmitToolCallResultRequest.parse(await request.json())
    submitted.push(body)
    return route(request, body)
  }
  return {
    client: createOmnaraClient({ baseUrl: 'https://api.example.com/v1', fetch }),
    requests,
    submitted,
  }
}

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
    void worker.handle({ tool_call_id: toolCallID, agent_id: agentID, state: 'ready' })

    await vi.waitFor(() => {
      expect(submitted).toEqual([found])
    })
    expect(lookup).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({ agentID, toolCallID, name: 'lookup_customer', input }),
    )
    expect(requests).toEqual([`GET ${callPath}`, `POST ${callPath}/result`])
    expect(onError).not.toHaveBeenCalled()
  })

  it('skips webhook calls from agents outside the project or profile', async () => {
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const onError = vi.fn()
    const otherProfile = api(() => new Response(null, { status: 500 }))
    const otherProject = api(
      () => new Response(null, { status: 500 }),
      () => Response.json({ error: 'not found', code: 'not_found' }, { status: 404 }),
    )
    void createCustomToolWorker({
      client: otherProfile.client,
      path,
      agentProfileID: `aprf_${'b'.repeat(26)}`,
      tools: { lookup_customer: lookup },
      onError,
    }).handle(ready)
    void createCustomToolWorker({
      client: otherProject.client,
      path,
      tools: { lookup_customer: lookup },
      onError,
    }).handle(ready)

    await vi.waitFor(() => {
      expect([...otherProfile.requests, ...otherProject.requests]).toEqual([
        `GET ${callPath}`,
        `GET ${agentPath}`,
        `GET ${callPath}`,
      ])
    })
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(lookup).not.toHaveBeenCalled()
    expect(onError).not.toHaveBeenCalled()
  })

  it('submits handler errors and rejected results as failures', async () => {
    const rankID = `tcl_${'b'.repeat(26)}`
    const sizeID = `tcl_${'c'.repeat(26)}`
    const { client, submitted } = api((request, body = found) => {
      if (body.outcome === 'failed') return accepted(body)
      return request.url.includes(sizeID)
        ? Response.json({ error: 'request too large', code: 'request_too_large' }, { status: 413 })
        : Response.json({ error: 'invalid result', code: 'validation_failed' }, { status: 400 })
    })
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
      return Response.json({ data: calls, next_cursor: null })
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

  it('waits for running tools and stops taking work when polling fails', async () => {
    const { client } = api(() =>
      Response.json({ error: 'forbidden', code: 'forbidden' }, { status: 403 }),
    )
    const authenticated: string[] = []
    client.interceptors.request.use((request) => {
      authenticated.push(request.url)
      return request
    })
    const release: ((result: SubmitToolCallResultRequest) => void)[] = []
    const lookup = vi.fn<CustomToolHandler>(
      () =>
        new Promise((resolve) => {
          release.push(resolve)
        }),
    )
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: lookup },
      onError: vi.fn(),
    })

    void worker.handle(ready)
    await vi.waitFor(() => {
      expect(release).toHaveLength(1)
    })
    let outcome: unknown
    const stopped = worker.run().catch((cause: unknown) => {
      outcome = cause
    })
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(outcome).toBeUndefined()
    void worker.handle({ ...ready, tool_call_id: `tcl_${'b'.repeat(26)}` })
    release[0]?.(found)
    await stopped
    expect(outcome).toMatchObject({ status: 403 })
    expect(lookup).toHaveBeenCalledTimes(1)
    expect(authenticated.filter((url) => url.includes('tcl_bbbb'))).toEqual([])
  })

  it('resends a failed result at the next regular check, even while busy', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
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
    await worker.handle({ tool_call_id: toolCallID, agent_id: agentID, state: 'completed' })
    await done

    expect(onError).not.toHaveBeenCalled()
    expect(requests).toEqual([`GET ${callPath}`])
  })

  it('resolves handle() to whether the call still needs work', async () => {
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
    expect(await worker.handle({ ...ready, tool_call_id: rankID })).toBe(false)
    expect(await worker.handle({ ...ready, tool_call_id: rankID, name: 'other_tool' })).toBe(true)
    release[0]?.(found)
    expect(await first).toBe(true)
    expect(await worker.handle({ ...ready, tool_call_id: deniedID })).toBe(false)
  })

  it('waits for started tools when a later page fails', async () => {
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

    let outcome: unknown
    const checked = worker.check().catch((cause: unknown) => {
      outcome = cause
    })
    await vi.waitFor(() => {
      expect(release).toHaveLength(1)
    })
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(outcome).toBeUndefined()
    release[0]?.(found)
    await checked
    expect(outcome).toMatchObject({ status: 403 })
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

    expect(await worker.handle(ready)).toBe(false)
    expect(onError).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ status: 404 }), {
      agentID,
      toolCallID,
      name: 'lookup_customer',
    })
    expect(await worker.handle({ ...ready, tool_call_id: rankID })).toBe(false)
    expect(await worker.handle(ready)).toBe(false)
    rejectSubmits = false
    vi.setSystemTime(Date.now() + 60_000)
    const before = requests.length
    expect(await worker.handle(ready)).toBe(true)
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

    expect(await worker.handle(ready)).toBe(false)
    const checked = worker.check()
    await vi.waitFor(() => {
      expect(submits).toBe(2)
    })
    await new Promise((resolve) => setTimeout(resolve, 0))
    list?.(Response.json({ data: [toolCall()], next_cursor: null }))
    await checked
    expect(await worker.handle(ready)).toBe(true)
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

    expect(await worker.handle(ready)).toBe(false)
    vi.setSystemTime(Date.now() + 60_000)
    const before = requests.length
    expect(
      await worker.handle({ tool_call_id: toolCallID, agent_id: agentID, state: 'completed' }),
    ).toBe(true)
    expect(requests).toHaveLength(before)
    expect(await worker.handle({ ...ready, tool_call_id: rankID })).toBe(true)
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
    void worker.handle({ tool_call_id: toolCallID, agent_id: agentID, state: 'completed' })
    expect(await first).toBe(true)
    const next = worker.handle({ ...ready, tool_call_id: rankID })
    await vi.waitFor(() => {
      expect(requests).toContain(`POST ${agentPath}/tool-calls/${rankID}/result`)
    })
    controller.abort()
    void worker.handle({ tool_call_id: rankID, agent_id: agentID, state: 'completed' })
    expect(await next).toBe(true)
  })

  it('bounds submits during shutdown and gives late results their own grace period', async () => {
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
      onError: vi.fn(),
    })

    const stopped = worker.run()
    await vi.waitFor(() => {
      expect(requests).toContain(`POST ${callPath}/result`)
    })
    controller.abort()
    await vi.advanceTimersByTimeAsync(21_000)
    expect(latePosts).toBe(2)

    let done = false
    void stopped.then(() => {
      done = true
    })
    await vi.advanceTimersByTimeAsync(5_000)
    expect(done).toBe(false)
    await vi.advanceTimersByTimeAsync(5_000)
    await stopped
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
      `GET ${agentPath}`,
      `POST ${callPath}/result`,
    ])
    expect(submitted).toEqual([found])
  })
})
