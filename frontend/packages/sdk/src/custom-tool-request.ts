import type { OmnaraClient } from './client'
import { ApiError } from './errors'
import { abortableDelay, retryDelay } from './retry'

const requestAttempts = 3

export class ToolRequestError extends Error {
  constructor(
    override readonly cause: Error,
    readonly retryAt: number,
  ) {
    super(cause.message, { cause })
  }
}

export function toolError(cause: unknown): Error {
  if (cause instanceof ToolRequestError) return cause.cause
  return cause instanceof Error ? cause : new Error(String(cause))
}

export function untilAborted<T>(request: () => Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise((resolve, reject) => {
    const abort = () => {
      signal.removeEventListener('abort', abort)
      reject(toolError(signal.reason))
    }
    if (signal.aborted) {
      abort()
      return
    }
    signal.addEventListener('abort', abort, { once: true })
    Promise.resolve()
      .then(() => {
        signal.throwIfAborted()
        return request()
      })
      .then(resolve, reject)
      .finally(() => {
        signal.removeEventListener('abort', abort)
      })
  })
}

export async function requestToolAPI<T>(
  client: OmnaraClient,
  request: (fetch: typeof globalThis.fetch, signal: AbortSignal) => Promise<T>,
  callerSignal: AbortSignal,
  timeoutMs: number,
): Promise<T> {
  const deadline = new AbortController()
  const expiresAt = Date.now() + timeoutMs
  const timer = setTimeout(() => {
    deadline.abort(new DOMException('Custom tool API request timed out', 'TimeoutError'))
  }, timeoutMs)
  const signal = AbortSignal.any([callerSignal, deadline.signal])
  let retryAt = 0
  try {
    for (let attempt = 1; ; attempt++) {
      const dispatched = { started: false }
      try {
        return await untilAborted(
          () =>
            request((input, init) => {
              // Authentication/interceptors may finish after the caller's deadline.
              signal.throwIfAborted()
              dispatched.started = true
              return (client.getConfig().fetch ?? globalThis.fetch)(input, init)
            }, signal),
          signal,
        )
      } catch (cause) {
        callerSignal.throwIfAborted()
        const error = toolError(cause)
        const retryable =
          deadline.signal.aborted ||
          (error instanceof ApiError
            ? error.status === 408 || error.status === 429 || error.status >= 500
            : dispatched.started && error instanceof TypeError)
        if (!retryable) throw error
        retryAt = Date.now() + (error instanceof ApiError ? (error.retryAfterMs ?? 0) : 0)
        const delay = Math.max(
          retryDelay(attempt),
          error instanceof ApiError ? (error.retryAfterMs ?? 0) : 0,
        )
        if (
          attempt >= requestAttempts ||
          deadline.signal.aborted ||
          Date.now() + delay >= expiresAt
        )
          throw error
        await abortableDelay(delay, signal)
      }
    }
  } catch (cause) {
    throw new ToolRequestError(toolError(cause), retryAt)
  } finally {
    clearTimeout(timer)
  }
}
