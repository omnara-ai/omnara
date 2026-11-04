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
const callPath = `${projectPath}/agents/${agentID}/tool-calls/${toolCallID}`
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

function toolCall(state: 'ready' | 'completed' = 'ready') {
  return {
    id: toolCallID,
    agent_id: agentID,
    turn_id: `trn_${idSuffix}`,
    provider_call_id: 'call_1',
    name: 'lookup_customer',
    input,
    type: 'custom',
    state,
    created_at: '2026-11-04T00:00:00Z',
  }
}

function accepted(body: SubmitToolCallResultRequest): Response {
  return Response.json(
    {
      tool_call: { ...toolCall('completed'), outcome: body.outcome },
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

function api(route: (request: Request, body?: SubmitToolCallResultRequest) => Response) {
  const requests: string[] = []
  const submitted: SubmitToolCallResultRequest[] = []
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const request = new Request(input, init)
    const url = new URL(request.url)
    requests.push(`${request.method} ${url.pathname}${url.search}`)
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
    expect(lookup).toHaveBeenCalledExactlyOnceWith({
      agentID,
      toolCallID,
      name: 'lookup_customer',
      input,
    })
    expect(requests).toEqual([`POST ${callPath}/result`])
    expect(onError).not.toHaveBeenCalled()
  })

  it('fetches the input when the update omits it', async () => {
    const { client, requests, submitted } = api((_, body) =>
      body === undefined ? Response.json(toolCall()) : accepted(body),
    )
    const lookup = vi.fn<CustomToolHandler>(() => Promise.resolve(found))
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: lookup },
      onError: vi.fn(),
    })

    worker.handle({ ...ready, input: undefined })

    await vi.waitFor(() => {
      expect(submitted).toEqual([found])
    })
    expect(lookup).toHaveBeenCalledWith({ agentID, toolCallID, name: 'lookup_customer', input })
    expect(requests).toEqual([`GET ${callPath}`, `POST ${callPath}/result`])
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
      expect(requests).toHaveLength(3)
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
    expect(requests).toEqual([])
    expect(onError).not.toHaveBeenCalled()
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
      agentProfileID: `aprf_${idSuffix}`,
      tools: { lookup_customer: () => Promise.resolve(found) },
      signal: controller.signal,
      onError: vi.fn(),
    })

    await worker.run()

    const list = `GET ${projectPath}/tool-calls?state=ready&type=custom&names=lookup_customer&agent_profile_id=aprf_${idSuffix}`
    expect(requests).toEqual([list, `${list}&cursor=next`, `POST ${callPath}/result`])
    expect(submitted).toEqual([found])
  })
})
