import { describe, expect, it, vi } from 'vitest'

import { controlCapability, controlReceipt, nextInstallationId } from '../control-test-support'
import type { ControlCompletion } from '../types'
import { WorkByteBudget } from '../work-budget'
import { CoreClient } from './client'
import { initialReceiptWorkBytes, ReceiptClientError } from './receipt-http'

const event = {
  provider_tenant_id: '123',
  event_id: 'provider-delivery',
  payload: { verified: true },
}

describe('generated control receipt transport', () => {
  it('durably submits under the original app with no fabricated installation scope', async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValue(Response.json(accepted('pending'), { status: 202 }))
    const receipt = controlReceipt()
    expect(
      await core(fetch).submitControlEvent(receipt.integration_app_id, event, signal()),
    ).toEqual(accepted('pending'))
    const request = capturedRequest(fetch.mock.calls[0])
    expect(request.url).toBe(
      `https://core.example.test/api/v1/channel-connector/apps/${receipt.integration_app_id}/control-events`,
    )
    expect(await request.json()).toEqual(event)
    expect(request.headers.get('authorization')).toBe('Bearer private-control-token')
    expect(request.redirect).toBe('error')
    expect(fetch).toHaveBeenCalledOnce()
  })

  it.each([200, 204])('requires HTTP 202 for durable acceptance, rejecting %s', async (status) => {
    const response =
      status === 204
        ? new Response(null, { status })
        : Response.json(accepted('pending'), { status })
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(response)
    await expect(
      core(fetch).submitControlEvent(controlReceipt().integration_app_id, event, signal()),
    ).rejects.toMatchObject({ code: 'invalid_response' })
    expect(fetch).toHaveBeenCalledOnce()
  })

  it('claims exactly one scoped receipt or no work, preserving lease and progress fields', async () => {
    const receipt = controlReceipt()
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(Response.json(receipt))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    const client = core(fetch)
    expect(await client.claimNextControlEvent(controlCapability, 30_000, signal())).toEqual(receipt)
    expect(await client.claimNextControlEvent(controlCapability, 30_000, signal())).toBeUndefined()
    const request = capturedRequest(fetch.mock.calls[0])
    expect(request.url).toBe(
      'https://core.example.test/api/v1/channel-connector/control-events/claim-next',
    )
    expect(await request.json()).toEqual({ capability: controlCapability, lease_ms: 30_000 })
    expect(request.redirect).toBe('error')
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it.each([
    { state: 'pending' },
    { lease_generation: Number.MAX_SAFE_INTEGER + 1 },
    { attempts_since_progress: Number.MAX_SAFE_INTEGER + 1 },
    { lease_token: 'invalid' },
    { receipt_id: 'irec_aaaaaaaaaaaaaaaaaaaaaaaaaa' },
    { integration_app_id: 'foreign' },
    { last_installation_id: nextInstallationId, end_installation_id: null },
  ])('rejects malformed control authority before returning a claim: %j', async (fields) => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValue(Response.json({ ...controlReceipt(), ...fields }))
    await expect(
      core(fetch).claimNextControlEvent(controlCapability, 30_000, signal()),
    ).rejects.toBeInstanceOf(ReceiptClientError)
    expect(fetch).toHaveBeenCalledOnce()
  })

  it.each(['transport', 'http'] as const)(
    'never retries an ambiguous %s claim or exposes credentials',
    async (kind) => {
      const fetch = vi.fn<typeof globalThis.fetch>(() =>
        kind === 'transport'
          ? Promise.reject(new TypeError('private-control-token'))
          : Promise.resolve(Response.json({ error: 'private-control-token' }, { status: 503 })),
      )
      const error = await core(fetch)
        .claimNextControlEvent(controlCapability, 30_000, signal())
        .catch((cause: unknown) => cause)
      expect(error).toBeInstanceOf(ReceiptClientError)
      expect(String(error)).not.toContain('private-control-token')
      expect(fetch).toHaveBeenCalledOnce()
    },
  )

  const completions: ControlCompletion[] = [
    { outcome: 'completed' },
    { outcome: 'yield', last_installation_id: nextInstallationId },
    {
      outcome: 'retry',
      last_installation_id: nextInstallationId,
      retry_after_ms: 60_000,
      last_error: { code: 'rate_limited' },
    },
    { outcome: 'failed', last_error: { code: 'malformed' } },
  ]
  it.each(completions)(
    'reports $outcome once with the original app/receipt/lease proof',
    async (completion) => {
      const receipt = controlReceipt()
      const state =
        completion.outcome === 'yield' || completion.outcome === 'retry'
          ? 'pending'
          : completion.outcome
      const response = {
        ...accepted(state),
        last_installation_id: completion.last_installation_id ?? receipt.last_installation_id,
      }
      const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(response))
      // Runtime values with extra proof fields cannot overwrite the original lease.
      const forged = { ...completion, lease_token: 'forged', lease_generation: 999 }
      await core(fetch).completeControlEvent(receipt, forged, signal())
      const request = capturedRequest(fetch.mock.calls[0])
      expect(request.url).toBe(
        `https://core.example.test/api/v1/channel-connector/apps/${receipt.integration_app_id}/control-events/${receipt.receipt_id}/complete`,
      )
      expect(await request.json()).toEqual({
        ...completion,
        lease_token: receipt.lease_token,
        lease_generation: receipt.lease_generation,
      })
      expect(request.redirect).toBe('error')
      expect(fetch).toHaveBeenCalledOnce()
    },
  )

  it.each([
    { receipt_id: 'icrc_bbbbbbbbbbbbbbbbbbbbbbbbbb' },
    { state: 'failed' },
    { last_installation_id: null },
    { end_installation_id: nextInstallationId },
  ])('rejects uncorrelated completion/progress without retry: %j', async (fields) => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
      Response.json({
        ...accepted('pending'),
        last_installation_id: nextInstallationId,
        ...fields,
      }),
    )
    await expect(
      core(fetch).completeControlEvent(
        controlReceipt(),
        { outcome: 'yield', last_installation_id: nextInstallationId },
        signal(),
      ),
    ).rejects.toMatchObject({ code: 'invalid_response' })
    expect(fetch).toHaveBeenCalledOnce()
  })

  it.each([204, 409, 503])(
    'does not retry ambiguous/rejected HTTP %s completion',
    async (status) => {
      const fetch = vi
        .fn<typeof globalThis.fetch>()
        .mockResolvedValue(
          status === 204 ? new Response(null, { status }) : Response.json({}, { status }),
        )
      await expect(
        core(fetch).completeControlEvent(controlReceipt(), { outcome: 'completed' }, signal()),
      ).rejects.toBeInstanceOf(ReceiptClientError)
      expect(fetch).toHaveBeenCalledOnce()
    },
  )

  it.each(['declared', 'streamed', 'parent capacity'] as const)(
    'bounds claim intake before decoding and cancels the body (%s)',
    async (kind) => {
      const canceled = vi.fn()
      const headers = new Headers({ 'content-type': 'application/json' })
      if (kind === 'declared') headers.set('content-length', String(96 * 1024 + 1))
      const response = new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(new Uint8Array(kind === 'streamed' ? 96 * 1024 + 1 : 4 * 1024))
          },
          cancel: canceled,
        }),
        { headers },
      )
      const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(response)
      const parent = new WorkByteBudget(initialReceiptWorkBytes)
      const child = new WorkByteBudget(2 * initialReceiptWorkBytes, parent)
      const work = child.reserve(initialReceiptWorkBytes)
      try {
        await expect(
          core(fetch).claimNextControlEvent(
            controlCapability,
            30_000,
            signal(),
            kind === 'parent capacity' ? work : undefined,
          ),
        ).rejects.toBeInstanceOf(ReceiptClientError)
        expect(canceled).toHaveBeenCalledOnce()
        expect(fetch).toHaveBeenCalledOnce()
        expect([parent.usedBytes, child.usedBytes]).toEqual([
          initialReceiptWorkBytes,
          initialReceiptWorkBytes,
        ])
      } finally {
        work.release()
      }
      expect([parent.usedBytes, child.usedBytes]).toEqual([0, 0])
    },
  )

  it.each(['submit', 'complete'] as const)(
    'bounds the %s acknowledgement to 4KiB before parsing',
    async (operation) => {
      const canceled = vi.fn()
      const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
        new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              controller.enqueue(new Uint8Array(4_096 + 1))
            },
            cancel: canceled,
          }),
          { status: operation === 'submit' ? 202 : 200 },
        ),
      )
      const client = core(fetch)
      const request =
        operation === 'submit'
          ? client.submitControlEvent(controlReceipt().integration_app_id, event, signal())
          : client.completeControlEvent(controlReceipt(), { outcome: 'completed' }, signal())
      await expect(request).rejects.toMatchObject({ code: 'response_too_large' })
      expect(canceled).toHaveBeenCalledOnce()
      expect(fetch).toHaveBeenCalledOnce()
    },
  )

  it.each(['submit', 'claim', 'complete'] as const)(
    'aborts actual %s response intake without hidden retry',
    async (operation) => {
      const canceled = vi.fn()
      const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
        new Response(new ReadableStream<Uint8Array>({ cancel: canceled }), {
          status: operation === 'submit' ? 202 : 200,
        }),
      )
      const controller = new AbortController()
      const client = core(fetch)
      const request =
        operation === 'submit'
          ? client.submitControlEvent(controlReceipt().integration_app_id, event, controller.signal)
          : operation === 'claim'
            ? client.claimNextControlEvent(controlCapability, 30_000, controller.signal)
            : client.completeControlEvent(
                controlReceipt(),
                { outcome: 'completed' },
                controller.signal,
              )
      const rejected = expect(request).rejects.toMatchObject({ code: 'aborted' })
      await vi.waitFor(() => {
        expect(fetch).toHaveBeenCalledOnce()
      })
      controller.abort(new Error('private caller reason'))
      await rejected
      expect(canceled).toHaveBeenCalledOnce()
      expect(fetch).toHaveBeenCalledOnce()
    },
  )
})

function signal() {
  return new AbortController().signal
}

function accepted(state: 'pending' | 'completed' | 'failed') {
  const receipt = controlReceipt()
  return {
    receipt_id: receipt.receipt_id,
    state,
    last_installation_id: receipt.last_installation_id,
    end_installation_id: receipt.end_installation_id,
  }
}

function core(fetch: typeof globalThis.fetch) {
  return new CoreClient({
    baseUrl: 'https://core.example.test/api/v1',
    token: 'private-control-token',
    requestTimeoutMs: 1_000,
    fetch,
  })
}

function capturedRequest(call: Parameters<typeof globalThis.fetch> | undefined): Request {
  if (!call) throw new Error('request was not sent')
  const [input, init] = call
  return new Request(input, init)
}
