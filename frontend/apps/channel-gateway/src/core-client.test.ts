import { describe, expect, it, vi } from 'vitest'

import { CoreClient, isTransientCoreError } from './core-client'
import { testRuntimeUnit } from './gateway-test-fixtures'

describe('CoreClient', () => {
  it('classifies request timeouts as transient but not caller cancellation', () => {
    expect(isTransientCoreError(new DOMException('timed out', 'TimeoutError'))).toBe(true)
    expect(isTransientCoreError(new DOMException('canceled', 'AbortError'))).toBe(false)
  })

  it('sends exact connector/provider capability pairs when claiming work', async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(Response.json({ deliveries: [] }))
      .mockResolvedValueOnce(Response.json({ runtime_units: [] }))
    const client = new CoreClient({
      baseUrl: 'https://core.example.test/api/v1',
      fetch,
      requestTimeoutMs: 5_000,
      token: 'channel_connector_test',
    })
    const deliveryCapability = { connector_key: 'chat_sdk_v1', provider: 'discord' }
    const runtimeCapability = { connector_key: 'custom_v1', provider: 'github' }

    await client.claimDeliveries(deliveryCapability, 'gateway-a', 30_000, 25)
    await client.claimRuntimeUnits(runtimeCapability, 'gateway-a', 30_000, 25)

    expect(fetch.mock.calls.map(([input]) => requestUrl(input))).toEqual([
      'https://core.example.test/api/v1/channel-connector/deliveries/claim',
      'https://core.example.test/api/v1/channel-connector/runtime-units/claim',
    ])
    const bodies = await Promise.all(
      fetch.mock.calls.map(([input, init]) => requestBody(input, init)),
    )
    const capabilities = [deliveryCapability, runtimeCapability]
    for (const [index, body] of bodies.entries()) {
      expect(JSON.parse(body)).toEqual({
        capability: capabilities[index],
        lease_ms: 30_000,
        limit: 25,
        owner: 'gateway-a',
      })
    }
  })

  it('does not retry a fenced state transition conflict', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValueOnce(
      Response.json(
        {
          code: 'state_transition_conflict',
          error: 'configuration changed while credentials were read',
        },
        { status: 409 },
      ),
    )
    const client = new CoreClient({
      baseUrl: 'https://core.example.test/api/v1',
      fetch,
      requestTimeoutMs: 5_000,
      token: 'channel_connector_test',
    })

    await expect(
      client.getAppConfiguration('iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa'),
    ).rejects.toMatchObject({ code: 'state_transition_conflict', status: 409 })
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('does not retry a stable not-found response', async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValue(Response.json({ code: 'not_found', error: 'not found' }, { status: 404 }))
    const client = new CoreClient({
      baseUrl: 'https://core.example.test/api/v1',
      fetch,
      requestTimeoutMs: 5_000,
      token: 'channel_connector_test',
    })

    await expect(
      client.getAppConfiguration('iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa'),
    ).rejects.toMatchObject({ code: 'not_found', status: 404 })
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('retries identical webhook and runtime events after a service-unavailable response', async () => {
    const unavailable = () =>
      Response.json({ code: 'service_unavailable', error: 'service unavailable' }, { status: 503 })
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(unavailable())
      .mockResolvedValueOnce(new Response(null, { status: 200 }))
      .mockResolvedValueOnce(unavailable())
      .mockResolvedValueOnce(new Response(null, { status: 200 }))
    const client = new CoreClient({
      baseUrl: 'https://core.example.test/api/v1',
      fetch,
      random: () => 0,
      requestTimeoutMs: 5_000,
      token: 'channel_connector_test',
    })
    const event = {
      actor: { display_name: 'Ada', metadata: {}, ref: 'user-1' },
      content_blocks: [{ text: 'hello', type: 'text' }],
      conversation: {
        direct: false,
        kind: 'thread',
        mentioned: true,
        metadata: {},
        ref: 'thread-1',
      },
      event_type: 'message.created',
      external_account_ref: 'account-1',
      external_tenant_id: 'tenant-1',
      metadata: {},
      occurred_at: '2026-08-31T00:00:00Z',
      provider_event_id: 'event-1',
      version: 'v1',
    } satisfies Parameters<CoreClient['submitInbound']>[1]
    const runtime = testRuntimeUnit({
      id: 'irun_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      lease_generation: 7,
      lease_token: '00000000-0000-7000-8000-000000000001',
    })

    await client.submitInbound('iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa', event)
    await client.submitRuntimeInbound('iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa', runtime, event)

    expect(fetch).toHaveBeenCalledTimes(4)
    const bodies = await Promise.all(
      fetch.mock.calls.map(([input, init]) => requestBody(input, init)),
    )
    expect(bodies[0]).toEqual(bodies[1])
    expect(bodies[2]).toEqual(bodies[3])
    expect(JSON.parse(bodies[2] ?? '')).toMatchObject({
      event,
      lease_generation: runtime.lease_generation,
      lease_token: runtime.lease_token,
    })
  })
})

function requestUrl(input: RequestInfo | URL): string {
  if (input instanceof Request) return input.url
  if (input instanceof URL) return input.toString()
  return input
}

async function requestBody(input: RequestInfo | URL, init?: RequestInit): Promise<string> {
  return input instanceof Request ? input.clone().text() : new Response(init?.body).text()
}
