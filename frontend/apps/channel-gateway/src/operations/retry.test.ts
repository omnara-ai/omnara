import { once } from 'node:events'
import { createServer } from 'node:http'
import type { AddressInfo } from 'node:net'

import { afterEach, describe, expect, it, vi } from 'vitest'

import { ProviderDeliveryError } from '../types'
import {
  type OperationAttemptContext,
  OperationRetryError,
  parseRetryAfter,
  retryOperation,
} from './retry'

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

function clock(): void {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-09-14T12:00:00Z'))
  vi.spyOn(Math, 'random').mockReturnValue(1)
}

function options(durationMs = 10_000) {
  return { requestId: 'request-1', deadlineMs: Date.now() + durationMs }
}

function failure(cause: unknown): OperationRetryError {
  expect(cause).toBeInstanceOf(OperationRetryError)
  if (!(cause instanceof OperationRetryError)) throw new Error('unexpected operation error')
  return cause
}

describe('retryOperation', () => {
  it('makes the initial attempt plus three retries, with one identity, signal, and deadline', async () => {
    clock()
    const contexts: OperationAttemptContext[] = []
    const settings = options()
    const deadlineMs = settings.deadlineMs
    const startedAt = Date.now()
    const times: number[] = []
    const operation = vi.fn((context: OperationAttemptContext) => {
      contexts.push(context)
      times.push(Date.now() - startedAt)
      // The caller cannot change the identity/deadline of retries in progress.
      settings.requestId = 'changed'
      settings.deadlineMs += 1_000
      return Promise.reject(new ProviderDeliveryError('temporary', { retryable: true }))
    })
    const result = retryOperation(settings, operation).catch(failure)
    await vi.runAllTimersAsync()
    expect(failure(await result)).toMatchObject({
      code: 'retries_exhausted',
      attempts: 4,
      outcomeUnknown: false,
    })
    expect(times).toEqual([0, 250, 750, 1_750])
    expect(contexts.map((context) => context.attempt)).toEqual([1, 2, 3, 4])
    expect(contexts.every((context) => context.requestId === 'request-1')).toBe(true)
    expect(contexts.every((context) => context.deadlineMs === deadlineMs)).toBe(true)
    expect(new Set(contexts.map((context) => context.signal)).size).toBe(1)
    expect(contexts[0]?.signal.aborted).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('returns a successful attempt and clears timers', async () => {
    clock()
    const operation = vi
      .fn<(_: OperationAttemptContext) => Promise<string>>()
      .mockRejectedValueOnce(new ProviderDeliveryError('rate limit', { retryable: true }))
      .mockResolvedValueOnce('published')
    const result = retryOperation(options(), operation)
    await vi.runAllTimersAsync()
    expect(await result).toBe('published')
    expect(operation).toHaveBeenCalledTimes(2)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('honors Retry-After without clamping it to the shorter backoff', async () => {
    clock()
    const operation = vi
      .fn<(_: OperationAttemptContext) => Promise<string>>()
      .mockRejectedValueOnce(
        new ProviderDeliveryError('rate limit', { retryable: true, retryAfterMs: 3_000 }),
      )
      .mockResolvedValueOnce('sent')
    const result = retryOperation(options(), operation)
    await vi.advanceTimersByTimeAsync(2_999)
    expect(operation).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(await result).toBe('sent')
    expect(operation).toHaveBeenCalledTimes(2)
  })

  it('stops when Retry-After cannot fit inside the original deadline', async () => {
    clock()
    const operation = vi.fn(() => {
      return Promise.reject(
        new ProviderDeliveryError('rate limit', { retryable: true, retryAfterMs: 60_000 }),
      )
    })
    const result = await retryOperation(options(500), operation).catch(failure)
    expect(failure(result)).toMatchObject({ code: 'deadline_exceeded', outcomeUnknown: false })
    await vi.runAllTimersAsync()
    expect(operation).toHaveBeenCalledTimes(1)
  })

  it.each([
    new ProviderDeliveryError('Bearer secret-value', { retryable: false }),
    new ProviderDeliveryError('Bearer secret-value', { retryable: true, outcomeUnknown: true }),
    new Error('Bearer secret-value'),
  ])('does not retry permanent or unknown non-idempotent failures (%s)', async (error) => {
    clock()
    const operation = vi.fn(() => {
      return Promise.reject(error)
    })
    const result = await retryOperation(options(), operation).catch(failure)
    const unknown = !(error instanceof ProviderDeliveryError) || error.outcomeUnknown
    expect(failure(result)).toMatchObject({
      code: unknown ? 'outcome_unknown' : 'permanent_failure',
      outcomeUnknown: unknown,
      attempts: 1,
    })
    expect(String(result)).not.toContain('secret-value')
    expect(failure(result).cause).toBeUndefined()
    await vi.runAllTimersAsync()
    expect(operation).toHaveBeenCalledTimes(1)
  })

  it('allows an explicitly idempotent unknown attempt to resolve with the same request identity', async () => {
    clock()
    const operation = vi
      .fn<(_: OperationAttemptContext) => Promise<string>>()
      .mockRejectedValueOnce(
        new ProviderDeliveryError('lost response', { outcomeUnknown: true, retryable: true }),
      )
      .mockResolvedValueOnce('original publication')
    const result = retryOperation({ ...options(), idempotent: true }, operation)
    await vi.runAllTimersAsync()
    expect(await result).toBe('original publication')
    expect(operation.mock.calls.map(([context]) => context.requestId)).toEqual([
      'request-1',
      'request-1',
    ])
  })

  it('retains an earlier unknown outcome when an idempotent retry later fails permanently', async () => {
    clock()
    const operation = vi
      .fn<(_: OperationAttemptContext) => Promise<never>>()
      .mockRejectedValueOnce(
        new ProviderDeliveryError('lost response', { outcomeUnknown: true, retryable: true }),
      )
      .mockRejectedValueOnce(new ProviderDeliveryError('access revoked'))
    const result = retryOperation({ ...options(), idempotent: true }, operation).catch(failure)
    await vi.runAllTimersAsync()
    expect(failure(await result)).toMatchObject({ code: 'outcome_unknown', outcomeUnknown: true })
    expect(operation).toHaveBeenCalledTimes(2)
  })

  it('does not infer retryability just from idempotence', async () => {
    clock()
    const operation = vi.fn(() => {
      return Promise.reject(new Error('unclassified failure'))
    })
    const result = await retryOperation({ ...options(), idempotent: true }, operation).catch(
      failure,
    )
    expect(failure(result)).toMatchObject({ code: 'outcome_unknown', attempts: 1 })
    expect(operation).toHaveBeenCalledTimes(1)
  })

  it('aborts actual work when the one deadline expires during a later attempt', async () => {
    clock()
    const aborted = vi.fn()
    const operation = vi.fn(async ({ attempt, signal }: OperationAttemptContext) => {
      if (attempt === 1) throw new ProviderDeliveryError('temporary', { retryable: true })
      return new Promise<never>((_resolve, reject) => {
        signal.addEventListener('abort', () => {
          aborted()
          reject(new Error('I/O aborted'))
        })
      })
    })
    const result = retryOperation(options(500), operation).catch(failure)
    await vi.advanceTimersByTimeAsync(500)
    expect(failure(await result)).toMatchObject({
      code: 'deadline_exceeded',
      outcomeUnknown: true,
      attempts: 2,
    })
    expect(aborted).toHaveBeenCalledTimes(1)
    expect(operation).toHaveBeenCalledTimes(2)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('stops on caller cancellation during backoff without introducing an unknown outcome', async () => {
    clock()
    const parent = new AbortController()
    const operation = vi.fn(() => {
      return Promise.reject(
        new ProviderDeliveryError('rate limit', { retryable: true, retryAfterMs: 1_000 }),
      )
    })
    const result = retryOperation({ ...options(), signal: parent.signal }, operation).catch(failure)
    await vi.advanceTimersByTimeAsync(100)
    parent.abort(new Error('secret reason'))
    expect(failure(await result)).toMatchObject({ code: 'canceled', outcomeUnknown: false })
    await vi.runAllTimersAsync()
    expect(operation).toHaveBeenCalledTimes(1)
  })

  it('bounds an uncooperative adapter, aborts its signal, and observes late rejection', async () => {
    clock()
    let rejectWork: ((reason: Error) => void) | undefined
    let workSignal: AbortSignal | undefined
    const operation = vi.fn(({ signal }: OperationAttemptContext) => {
      workSignal = signal
      return new Promise<never>((_resolve, reject) => {
        rejectWork = reject
      })
    })
    const result = retryOperation(options(100), operation).catch(failure)
    await vi.advanceTimersByTimeAsync(100)
    expect(failure(await result)).toMatchObject({ code: 'deadline_exceeded', outcomeUnknown: true })
    expect(workSignal?.aborted).toBe(true)
    rejectWork?.(new Error('late provider failure'))
    await vi.runAllTimersAsync()
    expect(operation).toHaveBeenCalledTimes(1)
  })

  it('never starts expired, canceled, or invalid requests', async () => {
    clock()
    const canceled = new AbortController()
    canceled.abort()
    const operation = vi.fn(() => Promise.resolve('sent'))
    for (const settings of [
      options(0),
      { ...options(), signal: canceled.signal },
      { ...options(), requestId: '' },
      { ...options(), deadlineMs: Infinity },
      options(2_147_483_648),
    ]) {
      const result = await retryOperation(settings, operation).catch(failure)
      expect(failure(result)).toMatchObject({ outcomeUnknown: false, attempts: 0 })
    }
    expect(operation).not.toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('cancels a real HTTP response stream instead of leaving fetch running', async () => {
    let finishResponse: (() => void) | undefined
    const responseClosed = new Promise<void>((resolve) => {
      finishResponse = resolve
    })
    let requestStarted: (() => void) | undefined
    const started = new Promise<void>((resolve) => {
      requestStarted = resolve
    })
    let requests = 0
    const server = createServer((_request, response) => {
      requests += 1
      response.on('close', () => finishResponse?.())
      response.writeHead(200, { 'Content-Type': 'text/plain' })
      response.write('partial response')
      requestStarted?.()
    })
    server.listen(0, '127.0.0.1')
    await once(server, 'listening')
    // SAFETY: The server successfully listened on a numeric TCP port and host above.
    const address = server.address() as AddressInfo
    const parent = new AbortController()
    try {
      const result = retryOperation(
        { ...options(2_000), signal: parent.signal },
        async ({ signal }) => {
          const response = await fetch(`http://127.0.0.1:${address.port}`, { signal })
          return response.text()
        },
      ).catch(failure)
      await started
      parent.abort()
      expect(failure(await result)).toMatchObject({ code: 'canceled', outcomeUnknown: true })
      await responseClosed
      expect(requests).toBe(1)
    } finally {
      parent.abort()
      server.closeAllConnections()
      await new Promise<void>((resolve) =>
        server.close(() => {
          resolve()
        }),
      )
    }
  })
})

describe('parseRetryAfter', () => {
  it('handles seconds and HTTP dates without rounding a minimum delay down', () => {
    const now = Date.parse('2026-09-14T12:00:00Z')
    expect(parseRetryAfter(' 3 ', now)).toBe(3_000)
    expect(parseRetryAfter('Mon, 14 Sep 2026 12:00:05 GMT', now)).toBe(5_000)
    expect(parseRetryAfter('Mon, 14 Sep 2026 11:00:00 GMT', now)).toBe(0)
    expect(parseRetryAfter('99999999999999999999999999', now)).toBe(Infinity)
    for (const value of [null, '', '-1', '0.5', 'garbage']) {
      expect(parseRetryAfter(value, now)).toBeUndefined()
    }
  })
})
