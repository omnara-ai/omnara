import { createOmnaraClient } from './client'
import type { SubmitToolCallResultRequest, ToolCallUpdate } from './generated/types.gen'
import { zSubmitToolCallResultRequest } from './generated/zod.gen'

export const idSuffix = 'a'.repeat(26)
export const agentID = `agt_${idSuffix}`
export const toolCallID = `tcl_${idSuffix}`
export const path = { orgID: `org_${idSuffix}`, projectID: `proj_${idSuffix}` }
export const projectPath = `/v1/orgs/${path.orgID}/projects/${path.projectID}`
export const agentPath = `${projectPath}/agents/${agentID}`
export const callPath = `${agentPath}/tool-calls/${toolCallID}`
export const profileID = `aprf_${idSuffix}`
export const input = { email: 'ada@example.com' }
export const routing = {
  org_id: path.orgID,
  project_id: path.projectID,
  agent_profile_id: profileID,
  parent_agent_id: null,
  root_agent_id: agentID,
}
export const ready: ToolCallUpdate = {
  ...routing,
  tool_call_id: toolCallID,
  agent_id: agentID,
  state: 'ready',
  name: 'lookup_customer',
  input,
}
export const found: SubmitToolCallResultRequest = {
  outcome: 'succeeded',
  content_blocks: [{ type: 'text', text: 'found' }],
}

export function failed(): SubmitToolCallResultRequest {
  return {
    outcome: 'failed',
    content_blocks: [{ type: 'text', text: 'The tool could not complete the request.' }],
  }
}

export function toolCall(id = toolCallID) {
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

export function toolPage(request: Request, calls: ReturnType<typeof toolCall>[]) {
  const query = new URL(request.url).searchParams
  const after = query.get('cursor')?.replace(/^after:/, '')
  const available = calls.filter((call) => after === undefined || call.id > after)
  const limit = Number(query.get('limit') ?? 50)
  if (!Number.isInteger(limit) || limit < 1 || limit > 100) {
    return Response.json({ code: 'invalid_request', error: 'invalid limit' }, { status: 400 })
  }
  const data = available.slice(0, limit)
  return Response.json({
    data,
    next_cursor: available.length > limit ? `after:${data.at(-1)?.id}` : null,
  })
}

export function accepted(body: SubmitToolCallResultRequest): Response {
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

export function api(
  route: (request: Request, body?: SubmitToolCallResultRequest) => Response | Promise<Response>,
  call: (request: Request) => Response = (request) =>
    Response.json(toolCall(new URL(request.url).pathname.split('/').at(-1))),
) {
  const requests: string[] = []
  const submitted: SubmitToolCallResultRequest[] = []
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const request = new Request(input, init)
    request.signal.throwIfAborted()
    const url = new URL(request.url)
    requests.push(`${request.method} ${url.pathname}${url.search}`)
    if (/\/tool-calls\/tcl_[a-z2-7]{26}$/.test(url.pathname)) return call(request)
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
