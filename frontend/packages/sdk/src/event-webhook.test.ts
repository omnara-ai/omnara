import { describe, expect, it } from 'vitest'

import { ready } from './custom-tool-worker-test-support'
import { verifyEventWebhook } from './event-webhook'

const keyBytes = new Uint8Array(32).fill(7)
const signingSecret = btoa(String.fromCharCode(...keyBytes))
const payload = { event: 'tool_call_update', data: ready }

async function signed(body = JSON.stringify(payload), age = 0) {
  const timestamp = String(Math.floor(Date.now() / 1000) - age)
  const key = await crypto.subtle.importKey(
    'raw',
    keyBytes,
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['sign'],
  )
  const signature = await crypto.subtle.sign(
    'HMAC',
    key,
    new TextEncoder().encode(`delivery-id.${timestamp}.${body}`),
  )
  return {
    body,
    signingSecret,
    headers: {
      'Webhook-Id': 'delivery-id',
      'Webhook-Timestamp': timestamp,
      'Webhook-Signature': `v1,${btoa(String.fromCharCode(...new Uint8Array(signature)))}`,
    },
  }
}

describe('verifyEventWebhook', () => {
  it('verifies original text and returns a typed payload with standard Headers or an object', async () => {
    const request = await signed(JSON.stringify(payload, null, 2))
    expect(await verifyEventWebhook(request)).toEqual(payload)
    expect(await verifyEventWebhook({ ...request, headers: new Headers(request.headers) })).toEqual(
      payload,
    )
  })

  it('rejects a modified body, wrong key, missing signature, and old or future timestamps', async () => {
    const request = await signed()
    for (const bad of [
      { ...request, body: JSON.stringify(payload, null, 2) },
      { ...request, signingSecret: btoa(String.fromCharCode(...new Uint8Array(32).fill(8))) },
      { ...request, headers: {} },
      await signed(undefined, 600),
      await signed(undefined, -600),
    ])
      await expect(verifyEventWebhook(bad)).rejects.toThrow()
  })

  it('rejects a valid signature over a malformed event', async () => {
    const request = await signed('{"event":"tool_call_update","data":{}}')
    await expect(verifyEventWebhook(request)).rejects.toThrow()
  })

  it('accepts signed future event kinds and states without rejecting valid delivery', async () => {
    for (const future of [
      { event: 'future_event', data: { value: 1 } },
      { event: 'tool_call_update', data: { ...ready, state: 'future_state' } },
    ])
      expect(await verifyEventWebhook(await signed(JSON.stringify(future)))).toEqual(future)
  })
})
