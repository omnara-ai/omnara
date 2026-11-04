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
  data: { agent_id: agentID, tool_call_id: toolCallID, state: 'ready', name: 'text_length', input },
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
  const { app, worker } = createWebhookApp({
    client: createOmnaraClient({
      baseUrl: 'https://api.example.com/v1',
      fetch: apiFetch,
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
  return { post, worker }
}

test('rejects bad signatures and invalid payloads before calling the API', async () => {
  const { post } = receiver(async () => {
    throw new Error('API must not be called')
  })
  assert.equal((await post(payload, false)).status, 400)
  assert.equal((await post(payload, true, 600)).status, 400)
  assert.equal((await post({ event: 'unknown', data: {} })).status, 400)
})

test('acknowledges a ready update, then runs the tool and submits its result', async () => {
  let submit: (body: unknown) => void = () => {}
  const submitted = new Promise((resolve) => {
    submit = resolve
  })
  const { post } = receiver(async (url, init) => {
    const request = new Request(url, init)
    assert.equal(request.method, 'POST')
    submit(await request.json())
    return accepted()
  })
  assert.equal((await post()).status, 204)
  assert.deepEqual(await submitted, result)
})

test('picks up ready calls that no webhook reported', async () => {
  const controller = new AbortController()
  const { worker } = receiver(async (url, init) => {
    const request = new Request(url, init)
    if (request.method === 'GET') {
      return Response.json({ data: [readyCall()], next_cursor: null })
    }
    assert.deepEqual(await request.json(), result)
    controller.abort()
    return accepted()
  }, controller.signal)
  await worker.run()
  assert.equal(controller.signal.aborted, true)
})
