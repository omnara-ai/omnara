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
  route: (request: Request, body?: SubmitToolCallResultRequest) => Response,
  call = () => Response.json(toolCall()),
) {
  const requests: string[] = []
  const submitted: SubmitToolCallResultRequest[] = []
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const request = new Request(input, init)
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

    worker.handle(ready)
    worker.handle(ready)
    worker.handle({ ...ready, state: 'running' })
    worker.handle({ ...ready, name: 'other_tool' })
    worker.handle({ tool_call_id: toolCallID, agent_id: agentID, state: 'ready' })

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
    createCustomToolWorker({
      client: otherProfile.client,
      path,
      agentProfileID: `aprf_${'b'.repeat(26)}`,
      tools: { lookup_customer: lookup },
      onError,
    }).handle(ready)
    createCustomToolWorker({
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

  it('skips a late ready update for a call that was canceled', async () => {
    const { client, requests } = api(
      () => new Response(null, { status: 500 }),
      () => Response.json({ ...toolCall(), state: 'completed', outcome: 'canceled' }),
    )
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: lookup },
      onError,
    })

    worker.handle(ready)

    await vi.waitFor(() => {
      expect(requests).toEqual([`GET ${callPath}`])
    })
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(lookup).not.toHaveBeenCalled()
    expect(onError).not.toHaveBeenCalled()
  })

  it('submits handler errors and rejected results as failures', async () => {
    const { client, submitted } = api((_, body = found) =>
      body.outcome === 'succeeded'
        ? Response.json({ error: 'invalid result', code: 'validation_failed' }, { status: 400 })
        : accepted(body),
    )
    const crashed = new Error('crm unavailable')
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      tools: {
        lookup_customer: () => Promise.reject(crashed),
        rank_customer: () => Promise.resolve(found),
      },
      onError,
    })
    const rankID = `tcl_${'b'.repeat(26)}`

    worker.handle(ready)
    worker.handle({ ...ready, tool_call_id: rankID, name: 'rank_customer' })

    await vi.waitFor(() => {
      expect(submitted).toHaveLength(3)
    })
    expect(submitted).toEqual(
      expect.arrayContaining([failed('crm unavailable'), found, failed('invalid result')]),
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

    worker.handle(ready)

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

    worker.handle(ready)

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

    worker.handle(ready)

    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(lookup).not.toHaveBeenCalled()
    expect(requests).not.toContain(`POST ${callPath}/result`)
    expect(onError).not.toHaveBeenCalled()
  })

  it('does not report or submit a tool that stops on shutdown', async () => {
    const controller = new AbortController()
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
      signal: controller.signal,
      onError,
    })

    worker.handle(ready)
    await vi.waitFor(() => {
      expect(started).toBe(true)
    })
    controller.abort()

    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(onError).not.toHaveBeenCalled()
    expect(requests).toEqual([`GET ${callPath}`])
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
    const toolSignal = lookup.mock.calls[0]?.[0].signal

    release.get(toolCallID)?.(found)
    await vi.waitFor(() => {
      expect(release.has(rankID)).toBe(true)
    })

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
  })

  it('waits for running tools and stops taking work when polling fails', async () => {
    const { client } = api(() =>
      Response.json({ error: 'forbidden', code: 'forbidden' }, { status: 403 }),
    )
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

    worker.handle(ready)
    await vi.waitFor(() => {
      expect(release).toHaveLength(1)
    })
    let outcome: unknown
    const stopped = worker.run().catch((cause: unknown) => {
      outcome = cause
    })
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(outcome).toBeUndefined()
    worker.handle({ ...ready, tool_call_id: `tcl_${'b'.repeat(26)}` })
    release[0]?.(found)
    await stopped
    expect(outcome).toMatchObject({ status: 403 })
    expect(lookup).toHaveBeenCalledTimes(1)
  })

  it('retries a failed call at the next regular check, even while busy', async () => {
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
      concurrency: 1,
      signal: controller.signal,
      onError,
    })
    const attempts = () =>
      lookup.mock.calls.filter(([call]) => call.toolCallID === toolCallID).length

    const stopped = worker.run()
    await vi.advanceTimersByTimeAsync(50_000)
    expect(attempts()).toBe(1)
    await vi.advanceTimersByTimeAsync(20_000)
    expect(attempts()).toBe(2)
    expect(finished.size).toBeLessThan(others.length)

    controller.abort()
    await vi.advanceTimersByTimeAsync(10_000)
    await stopped
    expect(onError).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ status: 403 }), {
      agentID,
      toolCallID,
      name: 'lookup_customer',
    })
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
    expect(requests).toEqual([list, `${list}&cursor=next`, `POST ${callPath}/result`])
    expect(submitted).toEqual([found])
  })
})
