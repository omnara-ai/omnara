import { request as httpRequest } from 'node:http'

import { ApiError } from '@omnara/sdk'
import type { Message } from 'chat'
import { describe, expect, it, vi } from 'vitest'

import type { AppRuntimeRegistry } from './app-registry'
import { messageContentBlocks } from './chat-sdk-runtime'
import { GatewayServer } from './server'
import type { GatewayLogger, ProviderRuntime } from './types'
import { WorkByteBudget } from './work-budget'

const integrationAppId = 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa'

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
      expect(response.connection).toBe('close')
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
      expect(response.connection).toBe('close')
    } finally {
      await server.close()
    }
  })

  it('bounds a hung adapter and releases its runtime handle', async () => {
    const runtime = providerRuntime(() => new Promise<Response>(() => undefined))
    const { logger, port, release, server } = await startServer(runtime, { handlerTimeoutMs: 20 })
    try {
      const response = await fetch(
        `http://127.0.0.1:${port}/hooks/${integrationAppId}/discord/events`,
      )
      expect(response.status).toBe(500)
      expect(release).toHaveBeenCalledOnce()
      expect(logger.error).toHaveBeenCalledOnce()
      expect(logger.error.mock.calls[0]?.[0]).toBe('channel webhook request failed')
      expect(typeof logger.error.mock.calls[0]?.[1]?.error).toBe('string')
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

  it('acknowledges promptly while retaining the runtime until waitUntil work finishes', async () => {
    const background = deferred()
    const runtime = providerRuntime((_request, context) => {
      context.waitUntil(background.promise)
      return Promise.resolve(new Response('accepted', { status: 202 }))
    })
    const { port, release, server } = await startServer(runtime)
    try {
      const response = await fetch(
        `http://127.0.0.1:${port}/hooks/${integrationAppId}/discord/events`,
      )

      expect(response.status).toBe(202)
      expect(await response.text()).toBe('accepted')
      expect(release).not.toHaveBeenCalled()
      background.resolve()
      await vi.waitFor(() => {
        expect(release).toHaveBeenCalledOnce()
      })
    } finally {
      background.resolve()
      await server.close()
    }
  })

  it('lets acknowledged webhook background work drain during graceful shutdown', async () => {
    const background = deferred()
    let requestSignal: AbortSignal | undefined
    const runtime = providerRuntime((request, context) => {
      requestSignal = request.signal
      context.waitUntil(background.promise)
      return Promise.resolve(new Response('accepted', { status: 202 }))
    })
    const { port, release, server } = await startServer(runtime)
    const response = await fetch(
      `http://127.0.0.1:${port}/hooks/${integrationAppId}/discord/events`,
    )
    expect(response.status).toBe(202)

    const closing = server.close()
    await Promise.resolve()
    expect(requestSignal?.aborted).toBe(false)
    expect(release).not.toHaveBeenCalled()

    background.resolve()
    await closing
    expect(release).toHaveBeenCalledOnce()
  })

  it('aborts webhook work only after the shutdown drain deadline', async () => {
    const background = new Promise<void>(() => undefined)
    let requestSignal: AbortSignal | undefined
    const runtime = providerRuntime((request, context) => {
      requestSignal = request.signal
      context.waitUntil(background)
      return Promise.resolve(new Response('accepted', { status: 202 }))
    })
    const { logger, port, release, server } = await startServer(runtime, {
      httpShutdownTimeoutMs: 20,
    })
    const response = await fetch(
      `http://127.0.0.1:${port}/hooks/${integrationAppId}/discord/events`,
    )
    expect(response.status).toBe(202)

    await server.close()

    expect(requestSignal?.aborted).toBe(true)
    expect(release).toHaveBeenCalledOnce()
    expect(logger.warn).toHaveBeenCalledWith('channel gateway HTTP shutdown reached its deadline')
  })

  it('bounds concurrent webhooks and aggregate buffered request bodies', async () => {
    const background = deferred()
    const runtime = providerRuntime((_request, context) => {
      context.waitUntil(background.promise)
      return Promise.resolve(new Response('accepted', { status: 202 }))
    })
    const { port, release, server } = await startServer(runtime, {
      maxBufferedWorkBytes: 12,
      maxConcurrentRequests: 2,
    })
    try {
      const first = await fetch(
        `http://127.0.0.1:${port}/hooks/${integrationAppId}/discord/events`,
        { body: '1234', method: 'POST' },
      )
      expect(first.status).toBe(202)

      const buffered = await fetch(
        `http://127.0.0.1:${port}/hooks/${integrationAppId}/discord/events`,
        { body: '5678', method: 'POST' },
      )
      expect(buffered.status).toBe(503)
      expect(buffered.headers.get('retry-after')).toBe('1')

      const second = await fetch(
        `http://127.0.0.1:${port}/hooks/${integrationAppId}/discord/events`,
        { body: '5', method: 'POST' },
      )
      expect(second.status).toBe(202)

      const concurrent = await fetch(
        `http://127.0.0.1:${port}/hooks/${integrationAppId}/discord/events`,
      )
      expect(concurrent.status).toBe(503)
      expect(concurrent.headers.get('retry-after')).toBe('1')
      expect(concurrent.headers.get('connection')).toBe('close')

      background.resolve()
      await vi.waitFor(() => {
        expect(release).toHaveBeenCalledTimes(3)
      })
    } finally {
      background.resolve()
      await server.close()
    }
  })

  it('counts remote media expansion against the shared webhook work budget', async () => {
    const background = deferred()
    const message = {
      attachments: [
        {
          fetchData: () => Promise.resolve(Buffer.from([1, 2])),
          mimeType: 'image/png',
          name: 'tiny.png',
          size: 2,
        },
      ],
      text: '',
    } as unknown as Message
    const runtime = providerRuntime(async (_request, context) => {
      await messageContentBlocks(message, 4, 4, {
        fetchAttachmentData: (attachment) => {
          if (!attachment.fetchData) throw new Error('missing test attachment loader')
          return attachment.fetchData()
        },
        reserveWorkBytes: context.reserveWorkBytes,
      })
      context.waitUntil(background.promise)
      return new Response('accepted', { status: 202 })
    })
    const { port, server } = await startServer(runtime, {
      maxBufferedWorkBytes: 10,
      maxConcurrentRequests: 2,
    })
    try {
      const first = await fetch(
        `http://127.0.0.1:${port}/hooks/${integrationAppId}/discord/events`,
        { body: 'x', method: 'POST' },
      )
      expect(first.status).toBe(202)

      const second = await fetch(
        `http://127.0.0.1:${port}/hooks/${integrationAppId}/discord/events`,
        { body: 'y', method: 'POST' },
      )
      expect(second.status).toBe(503)
      expect(second.headers.get('retry-after')).toBe('1')

      background.resolve()
      await vi.waitFor(async () => {
        const metrics = await (await fetch(`http://127.0.0.1:${port}/metrics`)).text()
        expect(metrics).toContain('omnara_channel_gateway_buffered_work_bytes 0')
      })
    } finally {
      background.resolve()
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
      const metrics = await (await fetch(`http://127.0.0.1:${port}/metrics`)).text()
      expect(metrics).toContain('omnara_channel_gateway_active_webhook_requests 0')
      expect(metrics).toContain('omnara_channel_gateway_buffered_work_bytes 0')
    } finally {
      await server.close()
    }
  })
})

async function startServer(
  runtime: ProviderRuntime,
  overrides: {
    acquireGate?: Promise<void>
    acquireError?: Error
    bodyLimitBytes?: number
    handlerTimeoutMs?: number
    httpShutdownTimeoutMs?: number
    isReady?: () => boolean | Promise<boolean>
    maxBufferedWorkBytes?: number
    maxConcurrentRequests?: number
    provider?: string
  } = {},
): Promise<{
  logger: ReturnType<typeof testLogger>
  port: number
  registry: { acquire: ReturnType<typeof vi.fn> }
  release: ReturnType<typeof vi.fn>
  server: GatewayServer
}> {
  const release = vi.fn(() => Promise.resolve())
  const registry = {
    acquire: vi.fn(async () => {
      if (overrides.acquireError) return Promise.reject(overrides.acquireError)
      await overrides.acquireGate
      return Promise.resolve({
        configuration: { app: { provider: overrides.provider ?? 'discord' } },
        handleWebhook: (
          request: Request,
          context: Parameters<ProviderRuntime['handleWebhook']>[1],
        ) => runtime.handleWebhook(request, context),
        release,
        runtime,
      })
    }),
  }
  const logger = testLogger()
  const server = new GatewayServer({
    bodyLimitBytes: overrides.bodyLimitBytes ?? 1024,
    handlerTimeoutMs: overrides.handlerTimeoutMs ?? 1_000,
    httpShutdownTimeoutMs: overrides.httpShutdownTimeoutMs ?? 100,
    isReady: overrides.isReady,
    logger,
    maxConcurrentRequests: overrides.maxConcurrentRequests ?? 8,
    port: 0,
    publicUrl: 'https://channels.example.test',
    registry: registry as unknown as AppRuntimeRegistry,
    workBudget: new WorkByteBudget(overrides.maxBufferedWorkBytes ?? 2048),
  })
  const port = await server.listen()
  return { logger, port, registry, release, server }
}

function providerRuntime(
  handleWebhook: ProviderRuntime['handleWebhook'] = () => Promise.resolve(new Response()),
): ProviderRuntime {
  return {
    close: () => Promise.resolve(),
    handleWebhook,
    send: () => Promise.resolve({ providerMessageRef: '' }),
  }
}

async function streamedRequest(
  port: number,
  path: string,
  chunks: string[],
): Promise<{ body: string; status: number }> {
  return new Promise((resolve, reject) => {
    const request = httpRequest(
      {
        headers: { 'transfer-encoding': 'chunked' },
        host: '127.0.0.1',
        method: 'POST',
        path,
        port,
      },
      (response) => {
        let body = ''
        response.setEncoding('utf8')
        response.on('data', (chunk: string) => {
          body += chunk
        })
        response.on('end', () => {
          resolve({ body, status: response.statusCode ?? 0 })
        })
      },
    )
    request.on('error', reject)
    for (const chunk of chunks) request.write(chunk)
    request.end()
  })
}

async function incompleteRequest(
  port: number,
  path: string,
  headers: Record<string, string>,
): Promise<{ connection: string | undefined; status: number }> {
  return new Promise((resolve, reject) => {
    let response: { connection: string | undefined; status: number } | undefined
    const request = httpRequest(
      { headers, host: '127.0.0.1', method: 'POST', path, port },
      (incoming) => {
        response = {
          connection: incoming.headers.connection,
          status: incoming.statusCode ?? 0,
        }
        incoming.resume()
      },
    )
    const timeout = setTimeout(() => {
      request.destroy()
      reject(new Error('incomplete request connection was not closed'))
    }, 1_000)
    request.on('close', () => {
      clearTimeout(timeout)
      if (response) resolve(response)
      else reject(new Error('incomplete request closed before receiving a response'))
    })
    request.on('error', (error) => {
      if (!response) {
        clearTimeout(timeout)
        reject(error)
      }
    })
    request.write('a')
  })
}

function testLogger() {
  return {
    debug: vi.fn<GatewayLogger['debug']>(),
    error: vi.fn<GatewayLogger['error']>(),
    info: vi.fn<GatewayLogger['info']>(),
    warn: vi.fn<GatewayLogger['warn']>(),
  }
}

function deferred(): { promise: Promise<void>; resolve(): void } {
  let resolve!: () => void
  const promise = new Promise<void>((settle) => {
    resolve = settle
  })
  return { promise, resolve }
}
