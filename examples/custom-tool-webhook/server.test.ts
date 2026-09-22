import assert from 'node:assert/strict'
import { createHmac } from 'node:crypto'
import { test } from 'node:test'

import { createOmnaraClient } from '@omnara/sdk'

import { createWebhookApp } from './server.ts'

const secret = Buffer.alloc(32, 7).toString('base64')
const agentID = 'agt_5n6a2bfgik7mv4qtrwz3jehcyd'
const toolCallID = 'tcl_5n6a2bfgik7mv4qtrwz3jehcyd'
const payload = {
  event: 'tool_call_update',
  data: { agent_id: agentID, tool_call_id: toolCallID, state: 'ready' },
}

function receiver(apiFetch: typeof fetch) {
  const app = createWebhookApp({
    client: createOmnaraClient({
      baseUrl: 'https://api.example.com/v1',
      fetch: apiFetch,
    }),
    orgID: 'org_5n6a2bfgik7mv4qtrwz3jehcyd',
    projectID: 'proj_5n6a2bfgik7mv4qtrwz3jehcyd',
    signingSecret: secret,
  })
  return async (body: unknown = payload, valid = true, age = 0) => {
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
}

function readyCall() {
  return {
    id: toolCallID,
    agent_id: agentID,
    turn_id: 'trn_5n6a2bfgik7mv4qtrwz3jehcyd',
    provider_call_id: 'call_1',
    name: 'text_length',
    input: { text: 'hello 🌍' },
    type: 'custom',
    state: 'ready',
    created_at: new Date().toISOString(),
  }
}

test('rejects bad signatures and invalid payloads before calling the API', async () => {
  const post = receiver(async () => {
    throw new Error('API must not be called')
  })
  assert.equal((await post(payload, false)).status, 400)
  assert.equal((await post(payload, true, 600)).status, 400)
  assert.equal((await post({ event: 'unknown', data: {} })).status, 400)
})

test('executes the tool and submits its result', async () => {
  let submitted = false
  const post = receiver(async (input, init) => {
    const request = new Request(input, init)
    if (request.method === 'GET') {
      return Response.json({ data: [readyCall()], next_cursor: null })
    }
    submitted = true
    const result = {
      outcome: 'succeeded',
      content_blocks: [{ type: 'structured_data', value: { length: 7 } }],
    }
    assert.deepEqual(await request.json(), result)
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
  })
  assert.equal((await post()).status, 204)
  assert.equal(submitted, true)
})

test('paginates ready calls and returns a retryable failure if submission fails', async (t) => {
  t.mock.method(console, 'error', () => {})
  let pages = 0
  const post = receiver(async (input, init) => {
    const request = new Request(input, init)
    if (request.method === 'GET') {
      pages++
      return Response.json(
        new URL(request.url).searchParams.has('cursor')
          ? { data: [readyCall()], next_cursor: null }
          : { data: [], next_cursor: 'next-page' },
      )
    }
    return Response.json({ error: 'unavailable', code: 'internal' }, { status: 503 })
  })
  assert.equal((await post()).status, 500)
  assert.equal(pages, 2)
})

test('acknowledges a result won by another receiver', async () => {
  const post = receiver(async (input, init) => {
    if (new Request(input, init).method === 'GET') {
      return Response.json({ data: [readyCall()], next_cursor: null })
    }
    return Response.json({ error: 'already completed', code: 'conflict' }, { status: 409 })
  })
  assert.equal((await post()).status, 204)
})
