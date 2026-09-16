import { channel } from 'node:diagnostics_channel'
import { once } from 'node:events'
import { IncomingMessage, ServerResponse } from 'node:http'
import { connect } from 'node:net'

import { describe, expect, it, vi } from 'vitest'
import { z } from 'zod'

import {
  deferred,
  incompleteRequest,
  integrationAppId,
  providerRuntime,
  startServer,
  streamedRequest,
} from './server-test-support'

const webhookPath = `/hooks/${integrationAppId}/discord`

describe('channel gateway HTTP adapter', () => {
  it('keeps native Fetch globals when the server starts', async () => {
    const nativeRequest = globalThis.Request
    const nativeResponse = globalThis.Response
    const { server } = await startServer(providerRuntime())
    try {
      expect(globalThis.Request).toBe(nativeRequest)
      expect(globalThis.Response).toBe(nativeResponse)
    } finally {
      await server.close()
    }
  })

  it('forwards a large streamed body intact without a second body consumer', async () => {
    const chunks = Array.from({ length: 64 }, (_, index) => `${index}:${'x'.repeat(8192)}`)
    let received = ''
    const { port, release, server } = await startServer(
      providerRuntime(async (request) => {
        received = await request.text()
        return new Response('accepted')
      }),
      { bodyLimitBytes: 1024 * 1024, maxBufferedWorkBytes: 2 * 1024 * 1024 },
    )
    try {
      const response = await streamedRequest(port, webhookPath, chunks)
      expect(response).toEqual({ body: 'accepted', status: 200 })
      expect(received).toBe(chunks.join(''))
      expect(release).toHaveBeenCalledOnce()
      const metrics = await (await fetch(`http://127.0.0.1:${port}/metrics`)).text()
      expect(metrics).toContain('omnara_channel_gateway_buffered_work_bytes 0')
    } finally {
      await server.close()
    }
  })

  it.each(['', '/', '/events/%2Fkeep%20encoded?token=a%2Fb+c&token=%252F'])(
    'preserves the original webhook path/query and trusted origin: %s',
    async (suffix) => {
      let received: Request | undefined
      const { port, server } = await startServer(
        providerRuntime((request) => {
          received = request
          return Promise.resolve(new Response('accepted'))
        }),
      )
      try {
        const response = await streamedRequest(port, `${webhookPath}${suffix}`, [], {
          host: 'attacker.example',
          'x-forwarded-host': 'another-attacker.example',
          'x-forwarded-proto': 'http',
        })
        expect(response.status).toBe(200)
        expect(received?.headers.get('host')).toBe('attacker.example')
        expect(received?.url).toBe(`https://channels.example.test${webhookPath}${suffix}`)
      } finally {
        await server.close()
      }
    },
  )

  it.each([
    '/unknown',
    '/hooks/invalid/discord',
    `/hooks/${integrationAppId}`,
    `/hooks/%69${integrationAppId.slice(1)}/discord`,
    `/hooks/${integrationAppId}/%64iscord`,
  ])('rejects noncanonical routes without acquiring a runtime: %s', async (path) => {
    const { port, registry, server } = await startServer(providerRuntime())
    try {
      const response = await fetch(`http://127.0.0.1:${port}${path}`, {
        body: 'unused webhook body',
        method: 'POST',
      })
      expect(response.status).toBe(404)
      expect(await response.text()).toBe('not found')
      expect(registry.acquire).not.toHaveBeenCalled()
    } finally {
      await server.close()
    }
  })

  it.each(['POST', 'GET', 'HEAD'])(
    'finishes the response socket before closing an incomplete %s request',
    async (method) => {
      const { port, server } = await startServer(providerRuntime())
      const socket = connect(port, '127.0.0.1')
      const finished = deferred()
      const events: string[] = []
      let incomplete: boolean | undefined
      let status: number | undefined
      const responseFinish = channel('http.server.response.finish')
      const observe: Parameters<typeof responseFinish.subscribe>[0] = (message) => {
        const { request, response } = z
          .object({
            request: z.instanceof(IncomingMessage),
            response: z.instanceof(ServerResponse),
          })
          .parse(message)
        if (request.socket.localPort !== port) return
        incomplete = !request.complete
        status = response.statusCode
        events.push('response.finish')
        request.socket.once('finish', () => {
          events.push('socket.finish')
        })
        request.socket.once('close', () => {
          events.push('socket.close')
          finished.resolve()
        })
      }
      responseFinish.subscribe(observe)
      try {
        let response = ''
        socket.setEncoding('utf8')
        socket.on('data', (chunk: string) => {
          response += chunk
        })
        socket.setTimeout(1_000, () => {
          socket.destroy(new Error('incomplete request response did not close'))
        })
        const ended = once(socket, 'end')
        socket.write(
          `${method} /healthz HTTP/1.1\r\nHost: localhost\r\nContent-Length: 100\r\n\r\na`,
        )
        await ended
        await finished.promise
        expect(response).toMatch(/^HTTP\/1\.1 200 /)
        if (method === 'POST') expect(response).not.toMatch(/\r\nconnection: close\r\n/i)
        else expect(response).toMatch(/\r\nconnection: close\r\n/i)
        expect(response.split('\r\n\r\n')[1]).toBe(method === 'HEAD' ? '' : '{"ok":true}')
        expect(incomplete).toBe(true)
        expect(status).toBe(200)
        expect(events).toEqual(['response.finish', 'socket.finish', 'socket.close'])
      } finally {
        responseFinish.unsubscribe(observe)
        socket.destroy()
        await server.close()
      }
    },
  )

  it.each([
    ['/unknown', 404],
    ['/healthz', 200],
    ['/readyz', 200],
    ['/metrics', 200],
  ] as const)('closes incomplete bodies on %s after responding', async (path, status) => {
    const { port, server } = await startServer(providerRuntime())
    try {
      const response = await incompleteRequest(port, path, { 'content-length': '100' })
      expect(response).toMatchObject({ status })
    } finally {
      await server.close()
    }
  })

  it.each([204, 205, 304])('preserves empty provider responses with status %s', async (status) => {
    const cookies = [
      'first=one; Expires=Wed, 21 Oct 2037 07:28:00 GMT; Path=/',
      'second=two; Path=/; HttpOnly',
    ]
    const headers = new Headers()
    for (const cookie of cookies) headers.append('set-cookie', cookie)
    const { port, server } = await startServer(
      providerRuntime(() => Promise.resolve(new Response(null, { headers, status }))),
    )
    try {
      const response = await fetch(`http://127.0.0.1:${port}${webhookPath}`)
      expect(response.status).toBe(status)
      expect(await response.text()).toBe('')
      expect(response.headers.getSetCookie()).toEqual(cookies)
    } finally {
      await server.close()
    }
  })

  it.each([
    { body: null, method: 'GET', status: 204 },
    { body: 'provider body', method: 'HEAD', status: 202 },
  ])(
    'prevents provider headers from suppressing $method responses',
    async ({ body, method, status }) => {
      const { port, server } = await startServer(
        providerRuntime(() =>
          Promise.resolve(
            new Response(body, { headers: { 'x-hono-already-sent': 'true' }, status }),
          ),
        ),
      )
      try {
        const response = await fetch(`http://127.0.0.1:${port}${webhookPath}`, {
          method,
          signal: AbortSignal.timeout(500),
        })
        expect(response.status).toBe(status)
        expect(await response.text()).toBe('')
        expect(response.headers.has('x-hono-already-sent')).toBe(false)
      } finally {
        await server.close()
      }
    },
  )

  it('preserves HEAD semantics and health response headers', async () => {
    let method: string | undefined
    const { port, server } = await startServer(
      providerRuntime((request) => {
        method = request.method
        return Promise.resolve(new Response('provider body', { status: 202 }))
      }),
    )
    try {
      const webhook = await fetch(`http://127.0.0.1:${port}${webhookPath}`, { method: 'HEAD' })
      expect(webhook.status).toBe(202)
      expect(await webhook.text()).toBe('')
      expect(method).toBe('HEAD')

      const health = await fetch(`http://127.0.0.1:${port}/healthz`, { method: 'HEAD' })
      expect(health.status).toBe(200)
      expect(health.headers.get('cache-control')).toBe('no-store')
      expect(health.headers.get('content-type')).toBe('application/json')
      expect(await health.text()).toBe('')
    } finally {
      await server.close()
    }
  })

  it.each([new Error('private provider details'), 'private provider details'])(
    'logs unexpected provider failures without exposing them: %s',
    async (failure) => {
      const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
      const { logger, port, release, server } = await startServer(
        providerRuntime(() => {
          // eslint-disable-next-line @typescript-eslint/only-throw-error -- Verify non-Error provider failures at the HTTP boundary.
          throw failure
        }),
      )
      try {
        const response = await fetch(`http://127.0.0.1:${port}${webhookPath}`)
        expect(response.status).toBe(500)
        expect(await response.text()).toBe('internal server error')
        expect(logger.error).toHaveBeenCalledExactlyOnceWith('channel webhook request failed', {
          error: 'private provider details',
        })
        expect(release).toHaveBeenCalledOnce()
        expect(consoleError).not.toHaveBeenCalled()
      } finally {
        await server.close()
        consoleError.mockRestore()
      }
    },
  )

  it('handles invalid provider response headers before the Node adapter writes them', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const { logger, port, release, server } = await startServer(
      providerRuntime(() =>
        Promise.resolve(
          new Response('private body', { headers: { 'x-provider-private': 'control\u0001value' } }),
        ),
      ),
    )
    try {
      const response = await fetch(`http://127.0.0.1:${port}${webhookPath}`, {
        body: 'body',
        method: 'POST',
      })
      expect(response.status).toBe(500)
      expect(await response.text()).toBe('internal server error')
      expect(logger.error).toHaveBeenCalledOnce()
      expect(logger.error.mock.calls[0]?.[0]).toBe('channel webhook request failed')
      expect(logger.error.mock.calls[0]?.[1]?.error).toContain('x-provider-private')
      expect(consoleError).not.toHaveBeenCalled()
      expect(release).toHaveBeenCalledOnce()
      const metrics = await (await fetch(`http://127.0.0.1:${port}/metrics`)).text()
      expect(metrics).toContain('omnara_channel_gateway_buffered_work_bytes 0')
      expect(metrics).toContain('omnara_channel_gateway_active_webhook_requests 0')
    } finally {
      await server.close()
      consoleError.mockRestore()
    }
  })

  it('serves HTTP/1.0 probes without a Host header', async () => {
    const { port, server } = await startServer(providerRuntime())
    const socket = connect(port, '127.0.0.1')
    try {
      let response = ''
      socket.setEncoding('utf8')
      socket.on('data', (chunk: string) => {
        response += chunk
      })
      socket.setTimeout(1_000, () => {
        socket.destroy(new Error('HTTP/1.0 response timed out'))
      })
      const ended = once(socket, 'end')
      socket.end('GET /healthz HTTP/1.0\r\n\r\n')
      await ended
      expect(response).toMatch(/^HTTP\/1\.[01] 200 /)
      expect(response).toContain('\r\n\r\n{"ok":true}')
    } finally {
      socket.destroy()
      await server.close()
    }
  })

  it('warns about malformed HTTP requests rejected before Hono routing', async () => {
    const { logger, port, registry, server } = await startServer(providerRuntime())
    try {
      const response = await incompleteRequest(port, webhookPath, {
        host: 'invalid host',
        'content-length': '100',
      })
      expect(response).toEqual({ connection: 'close', status: 400 })
      expect(logger.warn).toHaveBeenCalledOnce()
      expect(logger.warn.mock.calls[0]?.[0]).toBe('rejected malformed HTTP request')
      expect(logger.error).not.toHaveBeenCalled()
      expect(registry.acquire).not.toHaveBeenCalled()
    } finally {
      await server.close()
    }
  })

  it('sends rejections promptly while a retiring runtime is still releasing', async () => {
    const releaseGate = deferred()
    const { port, release, server } = await startServer(providerRuntime(), {
      provider: 'slack',
      releaseGate: releaseGate.promise,
    })
    try {
      const response = await fetch(`http://127.0.0.1:${port}${webhookPath}`, {
        signal: AbortSignal.timeout(500),
      })
      expect(response.status).toBe(404)
      expect(await response.text()).toBe('not found')
      expect(release).toHaveBeenCalledOnce()
      const retained = await (await fetch(`http://127.0.0.1:${port}/metrics`)).text()
      expect(retained).toContain('omnara_channel_gateway_active_webhook_requests 1')

      releaseGate.resolve()
      await vi.waitFor(async () => {
        const metrics = await (await fetch(`http://127.0.0.1:${port}/metrics`)).text()
        expect(metrics).toContain('omnara_channel_gateway_active_webhook_requests 0')
      })
    } finally {
      releaseGate.resolve()
      await server.close()
    }
  })
})
