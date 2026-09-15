import { abortableDelay, equalJitterMilliseconds } from './async'
import { ProviderDeliveryError } from './types'

export interface OperationAttemptContext {
  /** One-based attempt number. SDK-level automatic retries must be disabled. */
  attempt: number
  /** Stable across retries; reuse this value for provider idempotency keys. */
  requestId: string
  /** Absolute Unix milliseconds, unchanged across attempts and backoff. */
  deadlineMs: number
  /** Must reach actual provider requests, uploads, downloads, and response reads. */
  signal: AbortSignal
}

export interface OperationRetryOptions {
  requestId: string
  deadlineMs: number
  signal?: AbortSignal
  /** Enable only for reads or a provider-enforced key covering the whole operation. */
  idempotent?: boolean
}

export type OperationFailureCode =
  | 'invalid_request'
  | 'deadline_exceeded'
  | 'canceled'
  | 'permanent_failure'
  | 'retries_exhausted'
  | 'outcome_unknown'

/** Safe diagnostics: no provider error message, response, token, or cause is retained. */
export class OperationRetryError extends Error {
  readonly code: OperationFailureCode
  readonly outcomeUnknown: boolean
  readonly attempts: number

  constructor(code: OperationFailureCode, outcomeUnknown: boolean, attempts: number) {
    super(`channel operation failed: ${code}`)
    this.name = 'OperationRetryError'
    this.code = code
    this.outcomeUnknown = outcomeUnknown
    this.attempts = attempts
  }
}

/**
 * Initial attempt plus at most three retryable retries, all under one deadline.
 * ProviderDeliveryError is the adapter's explicit retry/outcome classification;
 * unclassified failures are unknown and never retried. Once unknown, a later
 * failed attempt cannot establish that the original mutation was never accepted.
 * Cancellation aborts the supplied I/O signal before rejecting the operation.
 * Adapters must honor that signal and disable SDK retries; this helper cannot
 * cancel an SDK that ignores its signal or count hidden SDK attempts.
 */
export async function retryOperation<T>(
  options: OperationRetryOptions,
  operation: (context: OperationAttemptContext) => Promise<T>,
): Promise<T> {
  const { requestId, deadlineMs, signal, idempotent = false } = options
  if (
    requestId.trim() === '' ||
    !Number.isSafeInteger(deadlineMs) ||
    deadlineMs - Date.now() > 2_147_483_647
  ) {
    throw new OperationRetryError('invalid_request', false, 0)
  }
  const controller = new AbortController()
  let attempts = 0
  let inFlight = false
  let outcomeUnknown = false
  let abortCode: 'canceled' | 'deadline_exceeded' = 'deadline_exceeded'
  const cancel = (): void => {
    abortCode = 'canceled'
    controller.abort()
  }
  const remainingMs = deadlineMs - Date.now()
  if (signal?.aborted) {
    throw new OperationRetryError('canceled', false, 0)
  }
  if (remainingMs <= 0) {
    throw new OperationRetryError('deadline_exceeded', false, 0)
  }
  signal?.addEventListener('abort', cancel, { once: true })
  const timer = setTimeout(() => {
    controller.abort()
  }, remainingMs)
  const abortedError = (): OperationRetryError =>
    new OperationRetryError(abortCode, outcomeUnknown || inFlight, attempts)

  try {
    for (;;) {
      if (isAborted(controller.signal)) throw abortedError()
      if (Date.now() >= deadlineMs) {
        controller.abort()
        throw abortedError()
      }
      attempts += 1
      inFlight = true
      try {
        const result = await runAttempt(
          () =>
            operation({
              attempt: attempts,
              requestId,
              deadlineMs,
              signal: controller.signal,
            }),
          controller.signal,
        )
        if (Date.now() >= deadlineMs) controller.abort()
        if (isAborted(controller.signal)) throw abortedError()
        inFlight = false
        return result
      } catch (error) {
        if (isAborted(controller.signal)) throw abortedError()
        inFlight = false
        const classified = error instanceof ProviderDeliveryError
        outcomeUnknown ||= !classified || error.outcomeUnknown
        if (outcomeUnknown && !idempotent) {
          throw new OperationRetryError('outcome_unknown', true, attempts)
        }
        if (!classified || !error.retryable) {
          throw new OperationRetryError(
            outcomeUnknown ? 'outcome_unknown' : 'permanent_failure',
            outcomeUnknown,
            attempts,
          )
        }
        if (attempts >= 4) {
          throw new OperationRetryError('retries_exhausted', outcomeUnknown, attempts)
        }
        const retryAfterMs = error.retryAfterMs ?? 0
        // Never truncate a provider's minimum delay and retry earlier. Malformed
        // retry guidance is not permission for an immediate retry either.
        if (!Number.isFinite(retryAfterMs) || retryAfterMs < 0) {
          throw new OperationRetryError('permanent_failure', outcomeUnknown, attempts)
        }
        const delayMs = Math.max(
          Math.ceil(retryAfterMs),
          equalJitterMilliseconds(Math.min(250 * 2 ** (attempts - 1), 1_000)),
        )
        if (delayMs >= deadlineMs - Date.now()) {
          throw new OperationRetryError('deadline_exceeded', outcomeUnknown, attempts)
        }
        if (!(await abortableDelay(delayMs, controller.signal))) throw abortedError()
      }
    }
  } finally {
    clearTimeout(timer)
    signal?.removeEventListener('abort', cancel)
    // Stop any leftover adapter work on every exit, including successful exits.
    controller.abort()
  }
}

/** Parse either HTTP Retry-After form to a minimum delay; invalid values are omitted. */
export function parseRetryAfter(
  value: string | null,
  nowMs: number = Date.now(),
): number | undefined {
  if (value === null || value.trim() === '') return undefined
  const normalized = value.trim()
  if (/^\d+$/.test(normalized)) {
    const milliseconds = Number(normalized) * 1_000
    // A huge but valid delay must stop retries, not disappear into default backoff.
    return Number.isSafeInteger(milliseconds) ? milliseconds : Infinity
  }
  // Require an HTTP-date shape, preventing Date.parse from treating "-1" or
  // decimal seconds as dates. Date.parse handles the three HTTP-date variants.
  if (!/^[A-Za-z]{3}(?:,|[a-z]+,| )/.test(normalized)) return undefined
  const timestamp = Date.parse(normalized)
  return Number.isFinite(timestamp) ? Math.max(0, timestamp - nowMs) : undefined
}

function runAttempt<T>(operation: () => Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const onAbort = (): void => {
      signal.removeEventListener('abort', onAbort)
      reject(new Error('operation aborted'))
    }
    if (signal.aborted) {
      onAbort()
      return
    }
    signal.addEventListener('abort', onAbort, { once: true })
    // Start synchronously after installing cancellation, and observe late SDK
    // rejection even when abort has already settled the bounded result.
    let work: Promise<T>
    try {
      work = operation()
    } catch (error) {
      signal.removeEventListener('abort', onAbort)
      reject(error instanceof Error ? error : new Error('provider threw a non-error value'))
      return
    }
    void work.then(resolve, reject).finally(() => {
      signal.removeEventListener('abort', onAbort)
    })
  })
}

function isAborted(signal: AbortSignal): boolean {
  return signal.aborted
}
