import { ApiError } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import {
  deferred,
  incompleteRequest,
  integrationAppId,
  providerRuntime,
  startServer,
  streamedRequest,
} from './server-test-support'

describe('channel webhook server', () => {
  it('forwards the raw request through the trusted public origin', async () => {
    let received: Request | undefined
    const runtime = providerRuntime((request) => {
      received = request
      return Promise.resolve(
        new Response(Uint8Array.from([9, 8, 7]), {
          headers: { 'x-provider-response': 'accepted' },
          status: 202,
        }),
      )
    })
    const { port, release, server } = await startServer(runtime)
    try {
      const response = await fetch(
        `http://127.0.0.1:${port}/hooks/${integrationAppId}/discord/events?challenge=1`,
        {
          body: Uint8Array.from([0, 1, 2, 255]),
          headers: { 'content-type': 'application/octet-stream', 'x-provider-signature': 'signed' },
          method: 'POST',
        },
      )

      expect(response.status).toBe(202)
      expect(response.headers.get('x-provider-response')).toBe('accepted')
      expect(response.headers.get('content-type')).toBe('text/plain; charset=UTF-8')
      expect([...new Uint8Array(await response.arrayBuffer())]).toEqual([9, 8, 7])
      expect(received?.url).toBe(
        `https://channels.example.test/hooks/${integrationAppId}/discord/events?challenge=1`,
      )
      if (!received) throw new Error('provider adapter did not receive the webhook request')
      expect(received.headers.get('x-provider-signature')).toBe('signed')
      expect([...new Uint8Array(await received.arrayBuffer())]).toEqual([0, 1, 2, 255])
      expect(release).toHaveBeenCalledOnce()
    } finally {
      await server.close()
    }
  })

  it('rejects provider mismatches and oversized streamed bodies without invoking an adapter', async () => {
    const handleWebhook = vi.fn(() => Promise.resolve(new Response()))
    const { port, registry, release, server } = await startServer(providerRuntime(handleWebhook), {
      bodyLimitBytes: 4,
      provider: 'slack',
    })
    try {
      const mismatch = await streamedRequest(port, `/hooks/${integrationAppId}/discord/events`, [
        'body',
        '-that-must-be-drained',
      ])
      expect(mismatch.status).toBe(404)
      expect(release).toHaveBeenCalledOnce()

      const oversized = await streamedRequest(port, `/hooks/${integrationAppId}/slack/events`, [
        'abc',
        'def',
      ])
      expect(oversized.status).toBe(413)
      expect(registry.acquire).toHaveBeenCalledTimes(2)
      expect(release).toHaveBeenCalledTimes(2)
      expect(handleWebhook).not.toHaveBeenCalled()
    } finally {
      await server.close()
    }
  })

  it('closes an incomplete declared-oversize request after flushing the rejection', async () => {
    const { port, server } = await startServer(providerRuntime(), { bodyLimitBytes: 4 })
    try {
      const response = await incompleteRequest(port, `/hooks/${integrationAppId}/discord/events`, {
        'content-length': '100',
      })

      expect(response.status).toBe(413)
    } finally {
      await server.close()
    }
  })

  it('times out and closes a slow incomplete request body', async () => {
    const { port, server } = await startServer(providerRuntime(), { handlerTimeoutMs: 30 })
    try {
      const response = await incompleteRequest(port, `/hooks/${integrationAppId}/discord/events`, {
        'transfer-encoding': 'chunked',
      })

      expect([408, 500]).toContain(response.status)
    } finally {
      await server.close()
    }
  })

  it('releases a runtime handle acquired after the request deadline', async () => {
    const acquisitionGate = deferred()
    const { port, release, server } = await startServer(providerRuntime(), {
      acquireGate: acquisitionGate.promise,
      handlerTimeoutMs: 20,
    })
    try {
      const response = await fetch(
        `http://127.0.0.1:${port}/hooks/${integrationAppId}/discord/events`,
      )
      expect(response.status).toBe(500)
      expect(release).not.toHaveBeenCalled()

      acquisitionGate.resolve()
      await vi.waitFor(() => {
        expect(release).toHaveBeenCalledOnce()
      })
    } finally {
      acquisitionGate.resolve()
      await server.close()
    }
  })

  it('rejects an oversized provider response and releases its runtime handle', async () => {
    const runtime = providerRuntime(() =>
      Promise.resolve(new Response(new Uint8Array(64 * 1024 + 1))),
    )
    const { logger, port, release, server } = await startServer(runtime)
    try {
      const response = await fetch(
        `http://127.0.0.1:${port}/hooks/${integrationAppId}/discord/events`,
      )

      expect(response.status).toBe(500)
      expect(release).toHaveBeenCalledOnce()
      expect(logger.error.mock.calls[0]?.[1]?.error).toContain('response body is too large')
    } finally {
      await server.close()
    }
  })

  it('reframes buffered provider responses and preserves separate cookies', async () => {
    const headers = new Headers({
      connection: 'x-provider-hop',
      'content-length': '1',
      'keep-alive': 'provider-timeout=999',
      'x-provider-hop': 'remove-me',
      'x-provider-response': 'keep-me',
    })
    headers.append('set-cookie', 'first=one; Path=/; HttpOnly')
    headers.append('set-cookie', 'second=two; Path=/; Secure')
    const runtime = providerRuntime(() =>
      Promise.resolve(new Response('complete provider body', { headers, status: 202 })),
    )
    const { port, server } = await startServer(runtime)
    try {
      const response = await fetch(
        `http://127.0.0.1:${port}/hooks/${integrationAppId}/discord/events`,
      )

      expect(response.status).toBe(202)
      expect(await response.text()).toBe('complete provider body')
      expect(response.headers.get('content-length')).not.toBe('1')
      expect(response.headers.get('keep-alive')).not.toBe('provider-timeout=999')
      expect(response.headers.get('x-provider-hop')).toBeNull()
      expect(response.headers.get('x-provider-response')).toBe('keep-me')
      expect(response.headers.getSetCookie()).toEqual([
        'first=one; Path=/; HttpOnly',
        'second=two; Path=/; Secure',
      ])
    } finally {
      await server.close()
    }
  })

  it('cancels a streaming provider response at the request deadline', async () => {
    let canceled = false
    const runtime = providerRuntime(() =>
      Promise.resolve(
        new Response(
          new ReadableStream({
            cancel: () => {
              canceled = true
            },
            pull: () => undefined,
          }),
        ),
      ),
    )
    const { port, release, server } = await startServer(runtime, { handlerTimeoutMs: 20 })
    try {
      const response = await fetch(
        `http://127.0.0.1:${port}/hooks/${integrationAppId}/discord/events`,
      )

      expect(response.status).toBe(500)
      await vi.waitFor(() => {
        expect(canceled).toBe(true)
      })
      expect(release).toHaveBeenCalledOnce()
    } finally {
      await server.close()
    }
  })

  it('returns not found for an unknown app without exposing core API details', async () => {
    const { port, server } = await startServer(providerRuntime(), {
      acquireError: new ApiError(404, 'internal lookup details'),
    })
    try {
      const response = await streamedRequest(port, `/hooks/${integrationAppId}/discord/events`, [
        'body',
        '-that-must-be-drained',
      ])
      expect(response.status).toBe(404)
      expect(response.body).toBe('not found')
    } finally {
      await server.close()
    }
  })

  it('accounts for the transient copy needed to join streamed body chunks', async () => {
    const { port, server } = await startServer(providerRuntime(), {
      bodyLimitBytes: 16,
      maxBufferedWorkBytes: 6,
    })
    try {
      const response = await streamedRequest(port, `/hooks/${integrationAppId}/discord/events`, [
        'ab',
        'cd',
      ])

      expect(response.status).toBe(503)
      expect(response.body).toBe('channel gateway is at capacity')
      const metrics = await (await fetch(`http://127.0.0.1:${port}/metrics`)).text()
      expect(metrics).toContain('omnara_channel_gateway_buffered_work_bytes 0')
    } finally {
      await server.close()
    }
  })

  it('reserves the copy made when a buffered body becomes a provider Request', async () => {
    const { port, server } = await startServer(providerRuntime(), {
      bodyLimitBytes: 16,
      maxBufferedWorkBytes: 7,
    })
    try {
      const response = await streamedRequest(port, `/hooks/${integrationAppId}/discord/events`, [
        'body',
      ])

      expect(response.status).toBe(503)
      expect(response.body).toBe('channel gateway is at capacity')
      const metrics = await (await fetch(`http://127.0.0.1:${port}/metrics`)).text()
      expect(metrics).toContain('omnara_channel_gateway_buffered_work_bytes 0')
    } finally {
      await server.close()
    }
  })

  it('reports readiness and bounded-resource metrics', async () => {
    let ready = false
    const { port, server } = await startServer(providerRuntime(), { isReady: () => ready })
    try {
      expect((await fetch(`http://127.0.0.1:${port}/readyz`)).status).toBe(503)
      ready = true
      expect((await fetch(`http://127.0.0.1:${port}/readyz`)).status).toBe(200)
      const metricsResponse = await fetch(`http://127.0.0.1:${port}/metrics`)
      expect(metricsResponse.headers.get('content-type')).toBe(
        'text/plain; version=0.0.4; charset=utf-8',
      )
      const metrics = await metricsResponse.text()
      expect(metrics).toContain('omnara_channel_gateway_active_webhook_requests 0')
      expect(metrics).toContain('omnara_channel_gateway_buffered_work_bytes 0')
    } finally {
      await server.close()
    }
  })
})
