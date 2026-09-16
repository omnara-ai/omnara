import { describe, expect, it, vi } from 'vitest'

import { raceWithAbort } from './async'
import { deferred, integrationAppId, providerRuntime, startServer } from './server-test-support'
import type { ProviderRuntime } from './types'

const webhookPath = `/hooks/${integrationAppId}/discord`

describe('actual webhook handler lifetime', () => {
  it.each(['response', 'rejection', 'cancel rejection'] as const)(
    'retains memory, admission and handle through timeout until the late %s settles',
    async (settlement) => {
      const gate = deferred()
      const canceled = vi.fn(() =>
        settlement === 'cancel rejection'
          ? Promise.reject(new Error('late stream cancellation rejected'))
          : Promise.resolve(),
      )
      let requestSignal: AbortSignal | undefined
      let retainedBody = ''
      const f = await startServer(
        providerRuntime(async (request, work) => {
          requestSignal = request.signal
          work.reserveWorkBytes(5)
          await gate.promise // Deliberately uncooperative with cancellation.
          retainedBody = await request.text()
          if (settlement === 'rejection') throw new Error('late private provider failure')
          return new Response(new ReadableStream({ cancel: canceled }))
        }),
        { handlerTimeoutMs: 30, maxConcurrentRequests: 1 },
      )
      try {
        const response = await fetch(`http://127.0.0.1:${f.port}${webhookPath}`, {
          method: 'POST',
          body: 'body',
          signal: AbortSignal.timeout(1_000),
        })
        expect(response.status).toBe(500)
        expect(await response.text()).toBe('internal server error')
        expect(requestSignal?.aborted).toBe(true)
        expect(f.workBudget.usedBytes).toBe(13) // body + Request copy + provider work
        expect(f.release).not.toHaveBeenCalled()
        const overloaded = await fetch(`http://127.0.0.1:${f.port}${webhookPath}`)
        expect(overloaded.status).toBe(503)
        await overloaded.text()
        expect(f.registry.acquire).toHaveBeenCalledOnce()

        gate.resolve()
        await vi.waitFor(() => {
          expect(f.release).toHaveBeenCalledOnce()
          expect(f.workBudget.usedBytes).toBe(0)
        })
        expect(retainedBody).toBe('body')
        expect(canceled).toHaveBeenCalledTimes(settlement === 'rejection' ? 0 : 1)
        const metrics = await (await fetch(`http://127.0.0.1:${f.port}/metrics`)).text()
        expect(metrics).toContain('omnara_channel_gateway_active_webhook_requests 0')
      } finally {
        gate.resolve()
        await f.server.close()
      }
    },
  )

  it('lets an actual in-flight handler finish during graceful shutdown', async () => {
    const entered = deferred()
    const gate = deferred()
    let requestSignal: AbortSignal | undefined
    const f = await startServer(
      providerRuntime(async (request, work) => {
        requestSignal = request.signal
        work.reserveWorkBytes(5)
        entered.resolve()
        await gate.promise
        return new Response('accepted', { status: 202 })
      }),
      { httpShutdownTimeoutMs: 500 },
    )
    const pending = fetch(`http://127.0.0.1:${f.port}${webhookPath}`)
    try {
      await entered.promise
      let closed = false
      const closing = f.server.close().then(() => {
        closed = true
      })
      await Promise.resolve()
      expect(closed).toBe(false)
      expect(requestSignal?.aborted).toBe(false)
      expect(f.release).not.toHaveBeenCalled()
      expect(f.workBudget.usedBytes).toBe(5)
      gate.resolve()
      const response = await pending
      expect(response.status).toBe(202)
      expect(await response.text()).toBe('accepted')
      await closing
      expect(f.release).toHaveBeenCalledOnce()
      expect(f.workBudget.usedBytes).toBe(0)
      expect(f.logger.warn).not.toHaveBeenCalled()
    } finally {
      gate.resolve()
      await pending.catch(() => undefined)
      await f.server.close()
    }
  })

  it('bounds shutdown while an aborted actual handler still owns its memory and handle', async () => {
    const entered = deferred()
    const gate = deferred()
    const canceled = vi.fn()
    let requestSignal: AbortSignal | undefined
    const f = await startServer(
      providerRuntime(async (request, work) => {
        requestSignal = request.signal
        work.reserveWorkBytes(5)
        entered.resolve()
        await gate.promise
        return new Response(new ReadableStream({ cancel: canceled }))
      }),
      { httpShutdownTimeoutMs: 20, handlerTimeoutMs: 5_000 },
    )
    const pending = fetch(`http://127.0.0.1:${f.port}${webhookPath}`, {
      method: 'POST',
      body: 'body',
    }).then(
      (response) => response.text(),
      () => undefined,
    )
    try {
      await entered.promise
      await raceWithAbort(f.server.close(), AbortSignal.timeout(500))
      expect(requestSignal?.aborted).toBe(true)
      expect(f.release).not.toHaveBeenCalled()
      expect(f.workBudget.usedBytes).toBe(13)
      expect(f.logger.warn).toHaveBeenCalledWith(
        'channel gateway HTTP shutdown reached its deadline',
      )
      gate.resolve()
      await vi.waitFor(() => {
        expect(f.release).toHaveBeenCalledOnce()
        expect(f.workBudget.usedBytes).toBe(0)
      })
      expect(canceled).toHaveBeenCalledOnce()
    } finally {
      gate.resolve()
      await pending
      await f.server.close()
    }
  })

  it('bounds concurrent actual handlers, request copies and provider buffer expansion', async () => {
    const gate = deferred()
    const handler = vi.fn<ProviderRuntime['handleWebhook']>(async (_request, work) => {
      work.reserveWorkBytes(3)
      await gate.promise
      return new Response('accepted', { status: 202 })
    })
    const f = await startServer(providerRuntime(handler), {
      maxBufferedWorkBytes: 17,
      maxConcurrentRequests: 2,
    })
    const first = fetch(`http://127.0.0.1:${f.port}${webhookPath}`, {
      body: '1234',
      method: 'POST',
    })
    let second: Promise<Response> | undefined
    try {
      await vi.waitFor(() => {
        expect(handler).toHaveBeenCalledOnce()
      })
      expect(f.workBudget.usedBytes).toBe(11)
      const buffered = await fetch(`http://127.0.0.1:${f.port}${webhookPath}`, {
        body: '5678',
        method: 'POST',
      })
      expect(buffered.status).toBe(503)
      expect(buffered.headers.get('retry-after')).toBe('1')
      await buffered.text()
      expect(handler).toHaveBeenCalledOnce()

      second = fetch(`http://127.0.0.1:${f.port}${webhookPath}`, { body: '5', method: 'POST' })
      await vi.waitFor(() => {
        expect(handler).toHaveBeenCalledTimes(2)
      })
      expect(f.workBudget.usedBytes).toBe(16)
      const concurrent = await fetch(`http://127.0.0.1:${f.port}${webhookPath}`)
      expect(concurrent.status).toBe(503)
      expect(concurrent.headers.get('connection')).toBe('close')
      await concurrent.text()
      gate.resolve()
      for (const response of await Promise.all([first, second])) {
        expect(response.status).toBe(202)
        expect(await response.text()).toBe('accepted')
      }
      await vi.waitFor(() => {
        expect(f.release).toHaveBeenCalledTimes(3)
        expect(f.workBudget.usedBytes).toBe(0)
      })
    } finally {
      gate.resolve()
      await Promise.allSettled([first, second])
      await f.server.close()
    }
  })
})
