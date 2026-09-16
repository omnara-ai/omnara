import { request as httpRequest } from 'node:http'

import { describe, expect, it, vi } from 'vitest'

import { deferred } from '../http/server-test-support'
import { operationRequestIdHeader, type OperationsOptions, operationsRoute } from './handler'
import { auth, completed, envelope, start } from './test-support'

const encoded = (id: string) => Buffer.from(id, 'utf8').toString('base64url')

// Never send/finish the body: early rejection must not wait for it or dispatch.
function headersOnly(port: number, correlation: string | string[]) {
  return new Promise<{ status: number; body: string }>((resolve, reject) => {
    const request = httpRequest(
      {
        host: '127.0.0.1',
        port,
        path: operationsRoute,
        method: 'POST',
        headers: {
          ...auth,
          [operationRequestIdHeader]: correlation,
          'content-type': 'application/json',
          'transfer-encoding': 'chunked',
        },
      },
      (response) => {
        let body = ''
        response.setEncoding('utf8')
        response.on('data', (chunk: string) => {
          body += chunk
        })
        response.on('error', reject)
        response.on('end', () => {
          resolve({ status: response.statusCode ?? 0, body })
          request.destroy()
        })
      },
    )
    request.on('error', reject)
    request.flushHeaders()
  })
}

describe('operation predispatch correlation', () => {
  it('rejects saturated admission before reading a body and preserves Unicode request IDs', async () => {
    const started = deferred()
    const release = deferred()
    const execute = vi.fn<OperationsOptions['execute']>(async () => {
      started.resolve()
      await release.promise
      return completed()
    })
    const { port, url } = await start(execute, { maxConcurrentRequests: 1 })
    const first = fetch(url, {
      method: 'POST',
      headers: { ...auth, 'content-type': 'application/json' },
      body: JSON.stringify(envelope()),
    })
    await started.promise
    try {
      for (const id of ['réquest-🧪', '界'.repeat(85) + 'a']) {
        const header = encoded(id)
        expect(header.length).toBeLessThanOrEqual(342)
        const response = await headersOnly(port, header)
        expect(response.status).toBe(503)
        expect(JSON.parse(response.body)).toEqual({ request_id: id, outcome: 'failed' })
        expect(execute).toHaveBeenCalledOnce()
      }
    } finally {
      release.resolve()
      await first
    }
  })

  it('correlates shared byte-capacity rejection without reading the body', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { port, workBudget } = await start(execute)
    const held = workBudget.reserve(workBudget.limitBytes)
    try {
      const response = await headersOnly(port, encoded('request-1'))
      expect(response.status).toBe(503)
      expect(JSON.parse(response.body)).toEqual({ request_id: 'request-1', outcome: 'failed' })
      expect(execute).not.toHaveBeenCalled()
    } finally {
      held.release()
    }
  })

  it.each([
    '',
    'not base64url',
    'A',
    'YR', // Nonzero unused bits: Buffer accepts it, canonical base64url must not.
    encoded('request-1') + '=',
    Buffer.from([255]).toString('base64url'),
    encoded('x'.repeat(257)),
    encoded('界'.repeat(86)),
    encoded(' leading'),
    encoded('trailing '),
    encoded('a\u0000b'),
    encoded('a\rb'),
    encoded('a\nb'),
    [encoded('request-1'), encoded('request-1')],
    [encoded('request-1'), encoded('request-2')],
  ])('rejects invalid or duplicate correlation without inventing proof: %j', async (header) => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { port } = await start(execute)
    const response = await headersOnly(port, header)
    expect(response.status).toBe(400)
    expect(JSON.parse(response.body)).toEqual({ error: 'invalid_operation' })
    expect(execute).not.toHaveBeenCalled()
  })

  it('does not echo correlation from an unauthenticated caller', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { url } = await start(execute)
    const response = await fetch(url, {
      method: 'POST',
      headers: { authorization: 'Bearer wrong', [operationRequestIdHeader]: encoded('private-id') },
      body: '{}',
    })
    expect(response.status).toBe(401)
    expect(await response.json()).toEqual({ error: 'unauthorized' })
    expect(execute).not.toHaveBeenCalled()
  })

  it('rejects a header/body identity mismatch before any dispatch', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { url } = await start(execute)
    const response = await fetch(url, {
      method: 'POST',
      headers: {
        ...auth,
        [operationRequestIdHeader]: encoded('request-header'),
        'content-type': 'application/json',
      },
      body: JSON.stringify({ ...envelope(), request_id: 'request-body' }),
    })
    expect(response.status).toBe(400)
    expect(await response.json()).toEqual({ request_id: 'request-header', outcome: 'failed' })
    expect(execute).not.toHaveBeenCalled()
  })

  it.each(['réquest-🧪', 'x\ty', '界'.repeat(85) + 'a'])(
    'dispatches only the matching original UTF-8 ID: %s',
    async (id) => {
      const execute = vi.fn<OperationsOptions['execute']>(completed)
      const { url } = await start(execute)
      const response = await fetch(url, {
        method: 'POST',
        headers: {
          ...auth,
          [operationRequestIdHeader]: encoded(id),
          'content-type': 'application/json',
        },
        body: JSON.stringify({ ...envelope(), request_id: id }),
      })
      expect(response.status).toBe(200)
      expect(await response.json()).toEqual({
        request_id: id,
        outcome: 'completed',
        payload: { publication: 'draft' },
      })
      expect(execute).toHaveBeenCalledOnce()
      expect(execute.mock.calls[0]?.[0].requestId).toBe(id)
    },
  )

  it('correlates malformed body rejection and preserves its HTTP status', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { url } = await start(execute)
    const response = await fetch(url, {
      method: 'POST',
      headers: {
        ...auth,
        [operationRequestIdHeader]: encoded('request-1'),
        'content-type': 'application/json',
      },
      body: '{} {}',
    })
    expect(response.status).toBe(400)
    expect(await response.json()).toEqual({ request_id: 'request-1', outcome: 'failed' })
    expect(execute).not.toHaveBeenCalled()
  })

  it('does not turn a provider-started ambiguous mutation into a predispatch failure', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(() => Promise.reject(new Error('lost ack')))
    const { url } = await start(execute)
    const response = await fetch(url, {
      method: 'POST',
      headers: {
        ...auth,
        [operationRequestIdHeader]: encoded('request-1'),
        'content-type': 'application/json',
      },
      body: JSON.stringify(envelope()),
    })
    expect(response.status).toBe(200)
    expect(await response.json()).toEqual({ request_id: 'request-1', outcome: 'unknown' })
    expect(execute).toHaveBeenCalledOnce()
  })
})
