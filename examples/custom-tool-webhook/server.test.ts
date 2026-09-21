import assert from 'node:assert/strict'
import { createHmac } from 'node:crypto'
import { once } from 'node:events'
import type { AddressInfo } from 'node:net'
import { test, type TestContext } from 'node:test'

import { createOmnaraClient } from '@omnara/sdk'

import { createWebhookServer } from './server.ts'

const secret = Buffer.alloc(32, 7).toString('base64')
const agentID = 'agt_5n6a2bfgik7mv4qtrwz3jehcyd'
const toolCallID = 'tcl_5n6a2bfgik7mv4qtrwz3jehcyd'
const payload = {
  event: 'tool_call_update',
  data: { agent_id: agentID, tool_call_id: toolCallID, state: 'ready' },
}

async function receiver(t: TestContext, apiFetch: typeof fetch) {
  const server = createWebhookServer({
    client: createOmnaraClient({
      baseUrl: 'https://api.example.com/v1',
      fetch: apiFetch,
    }),
    orgID: 'org_5n6a2bfgik7mv4qtrwz3jehcyd',
    projectID: 'proj_5n6a2bfgik7mv4qtrwz3jehcyd',
    signingSecret: secret,
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  t.after(() => {
    server.closeAllConnections()
    server.close()
  })
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}/webhook`
  return async (body: unknown = payload, valid = true, age = 0) => {
    const raw = JSON.stringify(body)
    const timestamp = String(Math.floor(Date.now() / 1000) - age)
    const signature = createHmac('sha256', Buffer.from(secret, 'base64'))
      .update(`delivery-id.${timestamp}.${raw}`)
      .digest('base64')
    return fetch(url, {
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

test('rejects bad signatures and invalid payloads before calling the API', async (t) => {
  const post = await receiver(t, async () => {
    throw new Error('API must not be called')
  })
  assert.equal((await post(payload, false)).status, 400)
  assert.equal((await post(payload, true, 600)).status, 400)
  assert.equal((await post({ event: 'unknown', data: {} })).status, 400)
})

test(
  'executes once for concurrent duplicates and skips completed calls',
  { timeout: 5000 },
  async (t) => {
    let submitted = 0
    let completed = false
    let release!: () => void
    let started!: () => void
    const submission = new Promise<void>((resolve) => {
      started = resolve
    })
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    const post = await receiver(t, async (input, init) => {
      const request = new Request(input, init)
      if (request.method === 'GET') {
        return Response.json({
          data: completed ? [] : [readyCall()],
          next_cursor: null,
        })
      }
      submitted++
      started()
      assert.deepEqual(await request.json(), {
        outcome: 'succeeded',
        content_blocks: [{ type: 'structured_data', value: { length: 7 } }],
      })
      await gate
      completed = true
      return Response.json(
        {
          tool_call: {
            ...readyCall(),
            state: 'completed',
            outcome: 'succeeded',
          },
          tool_result: {
            event_id: 'evt_5n6a2bfgik7mv4qtrwz3jehcyd',
            agent_id: agentID,
            tool_call_id: toolCallID,
            outcome: 'succeeded',
            content_blocks: [{ type: 'structured_data', value: { length: 7 } }],
            created_at: new Date().toISOString(),
          },
        },
        { status: 201 },
      )
    })
    const requests = [post(), post()]
    await submission
    release()
    assert.deepEqual(
      (await Promise.all(requests)).map((response) => response.status),
      [204, 204],
    )
    assert.equal(submitted, 1)
    assert.equal((await post()).status, 204)
    assert.equal(submitted, 1)
  },
)

test('paginates ready calls and returns a retryable failure if submission fails', async (t) => {
  t.mock.method(console, 'error', () => {})
  let pages = 0
  const post = await receiver(t, async (input, init) => {
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

test('acknowledges a result won by another receiver', async (t) => {
  const post = await receiver(t, async (input, init) => {
    if (new Request(input, init).method === 'GET') {
      return Response.json({ data: [readyCall()], next_cursor: null })
    }
    return Response.json({ error: 'already completed', code: 'conflict' }, { status: 409 })
  })
  assert.equal((await post()).status, 204)
})
