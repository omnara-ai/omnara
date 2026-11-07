import { forwardAbort, untilAborted } from './abort'
import { parseRetryAfter } from './errors'
import type { Config, RequestOptions } from './generated/client'
import { mergeHeaders } from './generated/client/utils.gen'
import {
  idempotentOperations,
  keyedOperations,
  knownOperations,
} from './generated/replay-contracts.gen'
import { notifyObserver } from './observer'
import type { RequestObservation, RequestPolicyOptions } from './request-policy-options'
import { responseWithStreamLifetime, streamWithLifetime } from './response-stream'
import { abortableDelay, retryAfterMaximumDelayMs, retryDelay } from './retry'

export function validateRequestPolicy(options: RequestPolicyOptions): void {
  for (const name of ['maxRetries', 'timeoutMs'] as const) {
    const value = options[name]
    if (value !== undefined && (!Number.isSafeInteger(value) || value < 0)) {
      throw new RangeError(`${name} must be a non-negative safe integer`)
    }
  }
  if (options.timeoutMs !== undefined && options.timeoutMs > 2_147_483_647) {
    throw new RangeError('timeoutMs must fit a 32-bit timer (at most 2147483647 ms)')
  }
}

// The generated merger handles Headers and records, but not HeadersInit tuples.
export function normalizeHeaders(
  headers: Config['headers'],
): Exclude<Config['headers'], unknown[]> {
  return Array.isArray(headers) ? new Headers(headers) : headers
}

/** A replay key belongs to one logical request, never to a client shared by many requests. */
export function validateClientRequestPolicy(
  options: RequestPolicyOptions & Pick<Config, 'headers'>,
): void {
  validateRequestPolicy(options)
  if (mergeHeaders(normalizeHeaders(options.headers)).has('Idempotency-Key')) {
    throw new TypeError(
      'Idempotency-Key cannot be set in client-wide headers; set it on each request instead.',
    )
  }
}

// Transport accepts arbitrary caller serializers; parsing here would change their payloads.
// oxlint-disable-next-line anti-slop/no-unknown-parameters
function oneShotBody(body: unknown): boolean {
  return (
    body instanceof ReadableStream ||
    // This is a conservative replay check, not a domain-data validator.
    // oxlint-disable-next-line anti-slop/no-runtime-typeof
    (body !== null && typeof body === 'object' && Symbol.asyncIterator in body)
  )
}

const retryableStatuses = new Set([408, 429, 500, 502, 503, 504])
type Overrides = Pick<RequestOptions, 'fetch' | 'signal' | 'headers' | 'bodySerializer'>
type PolicyRequest = Omit<Config, 'baseUrl'> & Pick<RequestOptions, 'url' | 'body'>
interface BackoffTiming {
  elapsedMs: number
  startedAt?: number
}

/** Run generated auth, serialization, interceptors and validation once. Only fetch is retried. */
// Preserve the generated client's conditional Promise return type, including
// throwOnError and responseStyle, rather than distributing its result union.
export function requestWithPolicy<T extends Promise<unknown>>(
  config: Config,
  options: PolicyRequest,
  dispatch: (overrides: Overrides) => T,
): T
export async function requestWithPolicy<T>(
  config: Config,
  options: PolicyRequest,
  dispatch: (overrides: Overrides) => Promise<T>,
): Promise<T> {
  const maxRetries = options.maxRetries ?? config.maxRetries ?? 2
  const timeoutMs = options.timeoutMs ?? config.timeoutMs ?? 0
  validateRequestPolicy({ maxRetries, timeoutMs })
  const startedAt = Date.now()
  const observer = options.onRequestComplete ?? config.onRequestComplete
  const method = (options.method ?? 'GET').toUpperCase()
  const operation = `${method} ${options.url}`
  const keyed = keyedOperations.has(operation)
  // Forward request overrides, including null removals, rather than merging the
  // config twice. The generated client performs the final merge.
  let headers = normalizeHeaders(options.headers)
  const mergedHeaders = mergeHeaders(normalizeHeaders(config.headers), headers)
  if (keyed && !mergedHeaders.has('Idempotency-Key')) {
    headers = {
      ...(headers instanceof Headers ? Object.fromEntries(headers) : headers),
      'Idempotency-Key': crypto.randomUUID(),
    }
  }

  const callerSignal = options.signal !== undefined ? options.signal : config.signal
  const controller = new AbortController()
  const disposeCaller = forwardAbort(callerSignal, controller)
  const { signal } = controller
  const timeoutReason =
    timeoutMs > 0 ? new DOMException('SDK request timed out', 'TimeoutError') : undefined
  const expiresAt = timeoutMs > 0 ? Date.now() + timeoutMs : Infinity
  const timer =
    timeoutMs > 0
      ? setTimeout(() => {
          if (signal.aborted) return
          controller.abort(timeoutReason)
        }, timeoutMs)
      : undefined
  const fetch = options.fetch ?? config.fetch ?? globalThis.fetch
  const parseAs = options.parseAs ?? config.parseAs
  let replayable = !oneShotBody(options.body)
  const serializer =
    options.bodySerializer !== undefined ? options.bodySerializer : config.bodySerializer
  const bodySerializer: Config['bodySerializer'] = serializer
    ? (body) => {
        const serialized = serializer(body)
        replayable &&= !oneShotBody(serialized)
        return serialized
      }
    : serializer
  let lastRequest: Request | undefined
  let lastResponse: Response | undefined
  let streamReturned = false
  let responseReturned = false
  let attempts = 0
  const backoff: BackoffTiming = { elapsedMs: 0 }
  let status: number | undefined
  let outcome: RequestObservation['outcome'] = 'returned'

  async function wait(delayMs: number) {
    const waitingAt = Date.now()
    backoff.startedAt = waitingAt
    try {
      await abortableDelay(delayMs, signal)
    } finally {
      backoff.elapsedMs += Math.max(0, Date.now() - waitingAt)
      backoff.startedAt = undefined
    }
  }

  function exposeResponse(response: Response): Response {
    if (parseAs === 'stream' && response.ok) clearTimeout(timer)
    lastResponse = response
    return response
  }

  const retryingFetch: typeof globalThis.fetch = async (input, init) => {
    // Auth/interceptors can finish after the deadline, even if they ignore cancellation.
    signal.throwIfAborted()
    const template =
      input instanceof Request && init === undefined ? input : new Request(input, init)
    const safe =
      replayable &&
      parseAs !== 'stream' &&
      (method === 'GET' ||
        method === 'HEAD' ||
        idempotentOperations.has(operation) ||
        (keyed && Boolean(template.headers.get('Idempotency-Key')?.trim())))
    const retries = safe ? maxRetries : 0
    try {
      for (let attempt = 0; ; attempt++) {
        signal.throwIfAborted()
        lastRequest = retries > 0 ? template.clone() : template
        lastResponse = undefined
        status = undefined
        let response: Response
        try {
          attempts++
          response = await fetch(lastRequest)
        } catch (error) {
          signal.throwIfAborted()
          const delay = retryDelay(attempt + 1)
          if (
            !(error instanceof TypeError) ||
            attempt >= retries ||
            Date.now() + delay >= expiresAt
          )
            throw error
          await wait(delay)
          continue
        }
        if (signal.aborted) {
          // A custom fetch can finish after cancellation even if it ignores signal.
          void response.body?.cancel().catch(() => undefined)
          signal.throwIfAborted()
        }
        lastResponse = response
        status = response.status
        if (!retryableStatuses.has(response.status) || attempt >= retries)
          return exposeResponse(response)
        const retryAfterMs = parseRetryAfter(response.headers.get('Retry-After')) ?? 0
        const delay = Math.max(retryDelay(attempt + 1), retryAfterMs)
        if (retryAfterMs > retryAfterMaximumDelayMs || Date.now() + delay >= expiresAt)
          return exposeResponse(response)
        // We will not expose this response. Release its connection before trying again.
        void response.body?.cancel().catch(() => undefined)
        lastResponse = undefined
        await wait(delay)
      }
    } finally {
      if (retries > 0) void template.body?.cancel().catch(() => undefined)
    }
  }

  try {
    const result = await untilAborted(
      () => dispatch({ fetch: retryingFetch, signal, headers, bodySerializer }),
      signal,
    )
    if (parseAs === 'stream') {
      // Wrap the final returned stream: response interceptors can clone or replace it.
      if (result instanceof ReadableStream) {
        const stream = callerSignal ? streamWithLifetime(result, signal, disposeCaller) : result
        streamReturned = true
        // SAFETY: generated data mode returns the raw stream; wrapping preserves that contract.
        return stream as T
      }
      // Generated fields mode is a transport envelope, not a domain payload.
      if (
        result !== null &&
        // oxlint-disable-next-line anti-slop/no-runtime-typeof
        typeof result === 'object' &&
        'response' in result &&
        result.response instanceof Response
      ) {
        responseReturned = result.response === lastResponse
        if (
          result.response.body !== null &&
          'data' in result &&
          result.data === result.response.body
        ) {
          const response = callerSignal
            ? responseWithStreamLifetime(result.response, signal, disposeCaller)
            : result.response
          streamReturned = true
          return { ...result, data: response.body, response }
        }
      }
    }
    return result
  } catch (error) {
    // The generated client handles its own errors. This also preserves its return
    // modes when our deadline wins a race against auth or response-body reading.
    if (options.throwOnError ?? config.throwOnError) {
      outcome = 'threw'
      throw error
    }
    const dataOnly = (options.responseStyle ?? config.responseStyle) === 'data'
    responseReturned = !dataOnly && lastResponse !== undefined
    // SAFETY: dispatch returns the generated RequestResult; these are exactly its
    // two non-throwing failure shapes selected by the same responseStyle option.
    return (dataOnly ? undefined : { error, request: lastRequest, response: lastResponse }) as T
  } finally {
    clearTimeout(timer)
    if (!streamReturned) {
      disposeCaller()
      // Fields-mode errors expose their Response for inspection. Cancellation still
      // releases the body when the caller/deadline has explicitly ended the request.
      if (parseAs === 'stream' && (!responseReturned || signal.aborted))
        void lastResponse?.body?.cancel().catch(() => undefined)
    }
    if (observer !== undefined) {
      notifyObserver(observer, {
        operation: knownOperations.has(operation) ? operation : 'unknown',
        durationMs: Math.max(0, Date.now() - startedAt),
        attempts,
        // Cancellation can settle the outer call before wait() reaches its finally block.
        backoffMs:
          backoff.elapsedMs +
          (backoff.startedAt === undefined ? 0 : Math.max(0, Date.now() - backoff.startedAt)),
        status,
        outcome: signal.aborted
          ? signal.reason === timeoutReason
            ? 'timeout'
            : 'canceled'
          : outcome,
      })
    }
  }
}
