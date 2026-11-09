import assert from 'node:assert/strict'
import { createHmac } from 'node:crypto'
import { test } from 'node:test'

import { createOmnaraClient } from '@omnara/sdk'

import { createWebhookApp } from './server.ts'

const secret = Buffer.alloc(32, 7).toString('base64')
const agentID = 'agt_5n6a2bfgik7mv4qtrwz3jehcyd'
const toolCallID = 'tcl_5n6a2bfgik7mv4qtrwz3jehcyd'
const input = { text: 'hello 🌍' }
const payload = {
  event: 'tool_call_update',
  data: {
    org_id: 'org_5n6a2bfgik7mv4qtrwz3jehcyd',
    project_id: 'proj_5n6a2bfgik7mv4qtrwz3jehcyd',
    agent_profile_id: null,
    parent_agent_id: null,
    root_agent_id: agentID,
    agent_id: agentID,
    tool_call_id: toolCallID,
    state: 'ready',
    name: 'text_length',
    input,
  },
}
const result = {
  outcome: 'succeeded',
  content_blocks: [{ type: 'structured_data', value: { length: 7 } }],
}

function readyCall() {
  return {
    id: toolCallID,
    agent_id: agentID,
    turn_id: 'trn_5n6a2bfgik7mv4qtrwz3jehcyd',
    provider_call_id: 'call_1',
    name: 'text_length',
    input,
    type: 'custom',
    state: 'ready',
    created_at: new Date().toISOString(),
  }
}

function accepted() {
  return Response.json(
    {
      tool_call: { ...readyCall(), state: 'completed', outcome: 'succeeded' },
      tool_result: {
        ...result,
        event_id: 'evt_5n6a2bfgik7mv4qtrwz3jehcyd',
        agent_id: agentID,
        tool_call_id: toolCallID,
        created_at: new Date().toISOString(),
      },
    },
    { status: 201 },
  )
}

function receiver(apiFetch: typeof fetch, signal?: AbortSignal) {
  const { app, recover, drain } = createWebhookApp({
    client: createOmnaraClient({
      baseUrl: 'https://api.example.com/v1',
      fetch: apiFetch,
      maxRetries: 0,
    }),
    orgID: 'org_5n6a2bfgik7mv4qtrwz3jehcyd',
    projectID: 'proj_5n6a2bfgik7mv4qtrwz3jehcyd',
    signingSecret: secret,
    signal,
  })
  const post = async (body: unknown = payload, valid = true, age = 0) => {
    const raw = JSON.stringify(body)
    const timestamp = String(Math.floor(Date.now() / 1000) - age)
    const signature = createHmac('sha256', Buffer.from(secret, 'base64'))
      .update(`delivery-id.${timestamp}.${raw}`)
      .digest('base64')
    return app.request('/webhook', {
      method: 'POST',
      body: raw,
      headers: {
        'webhook-id': 'delivery-id',
        'webhook-timestamp': timestamp,
        'webhook-signature': valid ? `v1,${signature}` : 'v1,invalid',
      },
    })
  }
  return { post, recover, drain }
}

test(
  'rejects bad signatures and invalid payloads before calling the API',
  { timeout: 5000 },
  async () => {
    const { post } = receiver(async () => {
      throw new Error('API must not be called')
    })
    assert.equal((await post(payload, false)).status, 400)
    assert.equal((await post(payload, true, 600)).status, 400)
    assert.equal((await post({ event: 'tool_call_update', data: {} })).status, 400)
    assert.equal((await post({ event: 'unknown', data: {} })).status, 204)
  },
)

test('acknowledges without waiting for result submission', { timeout: 5000 }, async () => {
  let release: () => void = () => {}
  const acceptedLater = new Promise<void>((resolve) => {
    release = resolve
  })
  let completed = false
  let submit: (body: unknown) => void = () => {}
  const submitted = new Promise((resolve) => {
    submit = resolve
  })
  const { post, drain } = receiver(async (url, init) => {
    const request = new Request(url, init)
    if (request.method === 'GET') return Response.json(readyCall())
    assert.equal(request.method, 'POST')
    submit(await request.json())
    await acceptedLater
    completed = true
    return accepted()
  })
  assert.equal((await post()).status, 204)
  assert.equal(completed, false)
  assert.deepEqual(await submitted, result)
  release()
  await drain()
  assert.equal(completed, true)
})

test('visits every recovery page promptly and sequentially', { timeout: 5000 }, async () => {
  const calls = Array.from({ length: 101 }, (_, i) => ({
    ...readyCall(),
    id: `tcl_${String.fromCharCode(97 + Math.floor(i / 26))}${String.fromCharCode(97 + (i % 26))}${'a'.repeat(24)}`,
  }))
  const submitted: string[] = []
  let lists = 0
  const { recover } = receiver(async (url, init) => {
    const request = new Request(url, init)
    const parsed = new URL(request.url)
    if (request.method === 'GET') {
      assert.ok(parsed.pathname.endsWith('/tool-calls'), 'discovery must not refetch each call')
      const start = Number(parsed.searchParams.get('cursor') ?? 0)
      assert.equal(submitted.length, start, 'finish the previous page before listing the next')
      lists++
      const limit = Number(parsed.searchParams.get('limit'))
      return Response.json({
        data: calls.slice(start, start + limit),
        next_cursor: start + limit < calls.length ? String(start + limit) : null,
      })
    }
    assert.deepEqual(await request.json(), result)
    submitted.push(parsed.pathname.split('/').at(-2) ?? '')
    return accepted()
  })
  await recover()
  assert.equal(lists, 3)
  assert.deepEqual(
    submitted,
    calls.map((call) => call.id),
  )
})

test(
  'recovers an explicitly reported delivery failure on the next application check',
  { timeout: 5000 },
  async (context) => {
    const errors: unknown[][] = []
    context.mock.method(console, 'error', (...args: unknown[]) => {
      errors.push(args)
    })
    let submissions = 0
    const { post, recover, drain } = receiver(async (url, init) => {
      const request = new Request(url, init)
      if (request.method === 'GET')
        return Response.json(
          new URL(request.url).pathname.endsWith('/tool-calls')
            ? { data: [readyCall()], next_cursor: null }
            : readyCall(),
        )
      submissions++
      assert.deepEqual(await request.json(), result)
      return submissions === 1
        ? Response.json({ code: 'service_unavailable', error: 'try again' }, { status: 503 })
        : accepted()
    })
    assert.equal((await post()).status, 204)
    await drain()
    assert.equal(submissions, 1)
    assert.equal(errors.length, 1)
    assert.match(String(errors[0]?.[0]), /next recovery pass will safely recompute/)
    await recover()
    assert.equal(submissions, 2)
  },
)

test(
  'reports permanent payload rejection as an application repair task',
  { timeout: 5000 },
  async (context) => {
    const errors: unknown[][] = []
    context.mock.method(console, 'error', (...args: unknown[]) => {
      errors.push(args)
    })
    let submissions = 0
    const { post, drain } = receiver(async (url, init) => {
      const request = new Request(url, init)
      if (request.method === 'GET') return Response.json(readyCall())
      submissions++
      return Response.json(
        { code: 'request_too_large', error: 'payload rejected' },
        { status: 413 },
      )
    })
    assert.equal((await post()).status, 204)
    await drain()
    assert.equal(submissions, 1)
    assert.equal(errors.length, 1)
    assert.match(String(errors[0]?.[0]), /submit a corrected or explicit failed result/)
  },
)

test('stops a recovery pass when canceled between pages', { timeout: 5000 }, async (context) => {
  const stop = new AbortController()
  const reason = new Error('application stopped')
  let lists = 0
  const { recover } = receiver(async (url, init) => {
    const request = new Request(url, init)
    if (request.method === 'GET') {
      lists++
      return Response.json({ data: [readyCall()], next_cursor: 'more' })
    }
    stop.abort(reason)
    return accepted()
  }, stop.signal)
  const errors: unknown[] = []
  context.mock.method(console, 'error', (...args: unknown[]) => {
    errors.push(args)
  })
  await assert.rejects(recover(), reason)
  assert.equal(lists, 1)
  assert.equal(errors.length, 1)
})
