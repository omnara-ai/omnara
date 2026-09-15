import type { ChannelConnectorEventReceipt } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import { CoreClient } from './core-client'
import { isTransientCoreError } from './core-http'
import { testRuntimeUnit } from './gateway-test-fixtures'
import {
  initialReceiptWorkBytes,
  maxReceiptResponseBytes,
  ReceiptClientError,
} from './receipt-http'
import { WorkByteBudget } from './work-budget'

describe('CoreClient', () => {
  it('classifies request timeouts as transient but not caller cancellation', () => {
    expect(isTransientCoreError(new DOMException('timed out', 'TimeoutError'))).toBe(true)
    expect(isTransientCoreError(new DOMException('canceled', 'AbortError'))).toBe(false)
  })

  it('sends exact connector/provider capability pairs when claiming work', async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(Response.json({ runtime_units: [] }))
    const client = new CoreClient({
      baseUrl: 'https://core.example.test/api/v1',
      fetch,
      requestTimeoutMs: 5_000,
      token: 'channel_connector_test',
    })
    const runtimeCapability = { connector_key: 'custom_v1', provider: 'github' }

    await client.claimRuntimeUnits(runtimeCapability, 'gateway-a', 30_000, 25)

    expect(fetch.mock.calls.map(([input]) => requestUrl(input))).toEqual([
      'https://core.example.test/api/v1/channel-connector/runtime-units/claim',
    ])
    const bodies = await Promise.all(
      fetch.mock.calls.map(([input, init]) => requestBody(input, init)),
    )
    const capabilities = [runtimeCapability]
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
      .mockResolvedValueOnce(Response.json(accepted(), { status: 202 }))
      .mockResolvedValueOnce(unavailable())
      .mockResolvedValueOnce(Response.json(accepted(), { status: 202 }))
    const client = new CoreClient({
      baseUrl: 'https://core.example.test/api/v1',
      fetch,
      random: () => 0,
      requestTimeoutMs: 5_000,
      token: 'channel_connector_test',
    })
    const event = inboundEvent()
    const runtime = testRuntimeUnit({
      id: 'irun_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      lease_generation: 7,
      lease_token: '00000000-0000-7000-8000-000000000001',
    })

    expect(await client.submitInbound('iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa', event)).toEqual(accepted())
    expect(
      await client.submitRuntimeInbound('iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa', runtime, event),
    ).toEqual(accepted())

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

  it.each([200, 204])('does not treat HTTP %s as durable receipt acceptance', async (status) => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValue(
        status === 204 ? new Response(null, { status }) : Response.json(accepted(), { status }),
      )
    await expect(
      receiptClient(fetch).submitInbound(receipt().integration_app_id, inboundEvent()),
    ).rejects.toMatchObject({ code: 'invalid_response' })
    expect(fetch).toHaveBeenCalledOnce()
  })

  it('snapshots an accepted event identity and payload across retries', async () => {
    const event = inboundEvent()
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockImplementationOnce(() => {
        event.event_id = 'changed'
        event.payload = { changed: true }
        return Promise.resolve(Response.json({}, { status: 503 }))
      })
      .mockResolvedValueOnce(Response.json(accepted(), { status: 202 }))
    await receiptClient(fetch).submitInbound(receipt().integration_app_id, event)
    const bodies = await Promise.all(
      fetch.mock.calls.map(([input, init]) => requestBody(input, init)),
    )
    expect(bodies[0]).toBe(bodies[1])
    expect(bodies[1]).toContain('event-1')
  })

  it('does not expose local serialization diagnostics or dispatch an invalid event', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>()
    const event = {
      ...inboundEvent(),
      payload: {
        toJSON: () => {
          throw new Error('private-receipt-token')
        },
      },
    }
    const client = receiptClient(fetch)
    for (const work of [
      client.submitInbound(receipt().integration_app_id, event),
      client.submitRuntimeInbound(receipt().integration_app_id, testRuntimeUnit(), event),
    ]) {
      const result = await work.catch((cause: unknown) => cause)
      expect(result).toBeInstanceOf(ReceiptClientError)
      expect(String(result)).not.toContain('private-receipt-token')
    }
    expect(fetch).not.toHaveBeenCalled()
  })

  it('claims exactly one generated receipt or no due work and sends the exact capability', async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(Response.json(receipt()))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    const client = receiptClient(fetch)
    const capability = { connector_key: 'chat_sdk_v1', provider: 'slack' }
    expect(await client.claimNextEvent(capability, 30_000)).toEqual(receipt())
    expect(await client.claimNextEvent(capability, 30_000)).toBeUndefined()
    const [input, init] = fetch.mock.calls[0] ?? []
    if (!input) throw new Error('missing request')
    expect(requestUrl(input)).toBe(
      'https://core.example.test/api/v1/channel-connector/events/claim-next',
    )
    expect(JSON.parse(await requestBody(input, init))).toEqual({ capability, lease_ms: 30_000 })
    expect(input).toBeInstanceOf(Request)
    if (input instanceof Request) {
      expect(input.redirect).toBe('error')
      expect(input.headers.get('authorization')).toBe('Bearer private-receipt-token')
    }
  })

  it.each([
    new TypeError('private-receipt-token'),
    Response.json({ error: 'private-receipt-token' }, { status: 503 }),
  ])('never retries an ambiguous claim and keeps errors free of credentials', async (failure) => {
    const fetch = vi.fn<typeof globalThis.fetch>(() =>
      failure instanceof Error ? Promise.reject(failure) : Promise.resolve(failure),
    )
    const result = await receiptClient(fetch)
      .claimNextEvent({ connector_key: 'chat_sdk_v1', provider: 'slack' }, 30_000)
      .catch((cause: unknown) => cause)
    expect(result).toBeInstanceOf(ReceiptClientError)
    expect(String(result)).not.toContain('private-receipt-token')
    expect(fetch).toHaveBeenCalledOnce()
  })

  it.each([
    { ...receipt(), state: 'pending' },
    { ...receipt(), lease_generation: Number.MAX_SAFE_INTEGER + 1 },
    { ...receipt(), lease_token: 'bad-lease' },
    { ...receipt(), integration_install_id: 'another-scope' },
  ])('rejects an invalid claimed receipt before returning authority', async (invalid) => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(invalid))
    await expect(
      receiptClient(fetch).claimNextEvent(
        { connector_key: 'chat_sdk_v1', provider: 'slack' },
        30_000,
      ),
    ).rejects.toBeInstanceOf(ReceiptClientError)
    expect(fetch).toHaveBeenCalledOnce()
  })

  it('completes through the original app/install/receipt path with the original fenced lease', async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValue(Response.json({ ...accepted(), state: 'failed' }))
    const claimed = receipt()
    await receiptClient(fetch).completeEvent(claimed, {
      state: 'failed',
      last_error: { code: 'permanent_failure' },
    })
    const [input, init] = fetch.mock.calls[0] ?? []
    if (!input) throw new Error('missing request')
    expect(requestUrl(input)).toContain(
      `/apps/${claimed.integration_app_id}/installations/${claimed.integration_install_id}/events/${claimed.receipt_id}/complete`,
    )
    expect(JSON.parse(await requestBody(input, init))).toEqual({
      state: 'failed',
      last_error: { code: 'permanent_failure' },
      lease_token: claimed.lease_token,
      lease_generation: claimed.lease_generation,
    })
  })

  it.each([
    new Response(null, { status: 204 }),
    Response.json({
      ...accepted(),
      receipt_id: 'irec_bbbbbbbbbbbbbbbbbbbbbbbbbb',
      state: 'completed',
    }),
    Response.json({ ...accepted(), state: 'processing' }),
    Response.json({ ...accepted(), state: 'failed' }),
  ])('rejects an uncorrelated completion acknowledgment without retrying', async (response) => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(response)
    await expect(
      receiptClient(fetch).completeEvent(receipt(), { state: 'completed' }),
    ).rejects.toMatchObject({ code: 'invalid_response' })
    expect(fetch).toHaveBeenCalledOnce()
  })

  it.each(['completed', 'pending', 'failed'] as const)(
    'accepts the generated correlated HTTP 200 %s completion',
    async (state) => {
      const fetch = vi
        .fn<typeof globalThis.fetch>()
        .mockResolvedValue(Response.json({ ...accepted(), state }))
      const completion: Parameters<CoreClient['completeEvent']>[1] = { state }
      if (state !== 'completed') completion.last_error = { code: 'fixture_failure' }
      await expect(
        receiptClient(fetch).completeEvent(receipt(), completion),
      ).resolves.toBeUndefined()
      expect(fetch).toHaveBeenCalledOnce()
    },
  )

  it.each([409, 503])('does not retry a completion response with HTTP %s', async (status) => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json({}, { status }))
    await expect(
      receiptClient(fetch).completeEvent(receipt(), { state: 'completed' }),
    ).rejects.toMatchObject({ code: 'http_error', status })
    expect(fetch).toHaveBeenCalledOnce()
  })

  it.each(['declared', 'streamed', 'work capacity'])(
    'cancels oversized claim responses before SDK parsing (%s)',
    async (kind) => {
      const canceled = vi.fn()
      const headers = new Headers({ 'content-type': 'application/json' })
      if (kind === 'declared') headers.set('content-length', String(maxReceiptResponseBytes + 1))
      const oversized = new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(
              new Uint8Array(kind === 'streamed' ? maxReceiptResponseBytes + 1 : 1024 * 1024),
            )
          },
          cancel: canceled,
        }),
        { headers },
      )
      const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(oversized)
      const work = new WorkByteBudget(initialReceiptWorkBytes).reserve(initialReceiptWorkBytes)
      try {
        await expect(
          receiptClient(fetch).claimNextEvent(
            { connector_key: 'chat_sdk_v1', provider: 'slack' },
            30_000,
            undefined,
            kind === 'work capacity' ? work : undefined,
          ),
        ).rejects.toBeInstanceOf(ReceiptClientError)
        expect(canceled).toHaveBeenCalledOnce()
        expect(fetch).toHaveBeenCalledOnce()
      } finally {
        work.release()
      }
    },
  )

  it('aborts actual receipt response intake on caller cancellation', async () => {
    const canceled = vi.fn()
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValue(new Response(new ReadableStream<Uint8Array>({ cancel: canceled })))
    const controller = new AbortController()
    const result = receiptClient(fetch).claimNextEvent(
      { connector_key: 'chat_sdk_v1', provider: 'slack' },
      30_000,
      controller.signal,
    )
    const rejected = expect(result).rejects.toMatchObject({ code: 'aborted' })
    await vi.waitFor(() => {
      expect(fetch).toHaveBeenCalledOnce()
    })
    controller.abort(new Error('private reason'))
    await rejected
    expect(canceled).toHaveBeenCalledOnce()
  })
})

function accepted() {
  return { receipt_id: 'irec_aaaaaaaaaaaaaaaaaaaaaaaaaa', state: 'pending' as const }
}
function inboundEvent(): Parameters<CoreClient['submitInbound']>[1] {
  return {
    event_id: 'event-1',
    integration_install_id: 'iin_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    payload: { text: 'verified event' },
  }
}
function receipt(): ChannelConnectorEventReceipt {
  return {
    ...accepted(),
    state: 'processing',
    integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    integration_install_id: 'iin_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    event_id: 'event-1',
    payload: { text: 'verified event' },
    lease_token: '00000000-0000-7000-8000-000000000001',
    lease_generation: 3,
    lease_expires_at: '2026-09-14T12:00:30Z',
    attempt_count: 1,
    last_error: {},
  }
}
function receiptClient(fetch: typeof globalThis.fetch) {
  return new CoreClient({
    baseUrl: 'https://core.example.test/api/v1',
    fetch,
    random: () => 0,
    token: 'private-receipt-token',
  })
}

function requestUrl(input: RequestInfo | URL): string {
  if (input instanceof Request) return input.url
  if (input instanceof URL) return input.toString()
  return input
}

async function requestBody(input: RequestInfo | URL, init?: RequestInit): Promise<string> {
  return input instanceof Request ? input.clone().text() : new Response(init?.body).text()
}
