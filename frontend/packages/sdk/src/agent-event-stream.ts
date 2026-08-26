import type { OmnaraClient } from './client'
import { ApiError, type ApiErrorCode } from './errors'
import { streamEvents } from './generated/sdk.gen'
import type {
  AgentEventStreamData,
  Error as ApiErrorBody,
  StreamEventsData,
} from './generated/types.gen'
import { zStreamEventsResponse } from './generated/zod.gen'
import { isUnknownEnumError } from './validate-response'

export type AgentEventStreamFrame = Exclude<AgentEventStreamData, ApiErrorBody>
export type AgentEventStreamErrorKind = 'api' | 'client' | 'contract' | 'http' | 'transport'

export class AgentEventStreamError extends Error {
  readonly kind: AgentEventStreamErrorKind
  readonly status: number | undefined
  readonly code: ApiErrorCode | undefined

  constructor({
    kind,
    message,
    status,
    code,
    cause,
  }: {
    kind: AgentEventStreamErrorKind
    message: string
    status?: number
    code?: ApiErrorCode
    cause?: unknown
  }) {
    super(message, { cause })
    this.name = 'AgentEventStreamError'
    this.kind = kind
    this.status = status
    this.code = code
  }
}

export type AgentEventStreamConnectionState =
  | { state: 'connected'; reconnected: boolean }
  | {
      state: 'reconnecting'
      attempt: number
      delayMs: number
      error: AgentEventStreamError
    }

export interface OpenAgentEventStreamOptions {
  client: OmnaraClient
  path: StreamEventsData['path']
  query?: StreamEventsData['query']
  headers?: StreamEventsData['headers']
  signal?: AbortSignal
  onConnectionStateChange?: (state: AgentEventStreamConnectionState) => void
}

const retryBaseDelayMs = 1_000
const retryMaximumDelayMs = 30_000
const retryAfterMaximumDelayMs = 60_000
const stableConnectionResetMs = 10_000
const activeReadTimeoutMs = 35_000

function isAborted(signal?: AbortSignal): boolean {
  return signal?.aborted === true
}

function streamError(
  kind: AgentEventStreamErrorKind,
  message: string,
  options: { status?: number; code?: ApiErrorCode; cause?: unknown } = {},
): AgentEventStreamError {
  return new AgentEventStreamError({ kind, message, ...options })
}

function retryable(error: AgentEventStreamError): boolean {
  if (error.kind === 'transport') return true
  if (error.kind === 'api') return error.code === 'service_unavailable'
  return (
    error.kind === 'http' &&
    (error.status === 408 || error.status === 429 || (error.status != null && error.status >= 500))
  )
}

function classifyAttemptError(
  error: unknown,
  { fetchInvoked, readTimedOut }: { fetchInvoked: boolean; readTimedOut: boolean },
): AgentEventStreamError {
  if (error instanceof AgentEventStreamError) return error
  if (readTimedOut) {
    return streamError('transport', 'Agent event stream timed out waiting for data', {
      cause: error,
    })
  }
  if (!fetchInvoked) {
    return streamError('client', 'Agent event stream request could not be prepared', {
      cause: error,
    })
  }
  return streamError('transport', 'Agent event stream disconnected', { cause: error })
}

function isApiErrorBody(data: AgentEventStreamData): data is ApiErrorBody {
  return (
    typeof data === 'object' &&
    'error' in data &&
    typeof data.error === 'string' &&
    'code' in data &&
    typeof data.code === 'string'
  )
}

function durableSequence(data: unknown): number | undefined {
  if (
    typeof data !== 'object' ||
    data == null ||
    !('event_kind' in data) ||
    typeof data.event_kind !== 'string' ||
    !('sequence' in data) ||
    typeof data.sequence !== 'number' ||
    !Number.isSafeInteger(data.sequence) ||
    data.sequence < 0
  ) {
    return undefined
  }
  return data.sequence
}

async function validateFrame(data: unknown): Promise<AgentEventStreamData> {
  const parsed = await zStreamEventsResponse.safeParseAsync(data)
  if (parsed.success) return data as AgentEventStreamData
  if (isUnknownEnumError(parsed.error, data)) return data as AgentEventStreamData
  throw streamError('contract', 'Agent event stream received data outside its API contract', {
    cause: parsed.error,
  })
}

function parseRetryAfter(value: string | null, now = Date.now()): number | undefined {
  if (value == null) return undefined
  const trimmed = value.trim()
  if (/^\d+$/.test(trimmed)) {
    return Math.min(Number.parseInt(trimmed, 10) * 1_000, retryAfterMaximumDelayMs)
  }
  const at = Date.parse(trimmed)
  if (Number.isNaN(at)) return undefined
  return Math.min(Math.max(at - now, 0), retryAfterMaximumDelayMs)
}

function reconnectDelay(attempt: number): number {
  const maximum = Math.min(retryBaseDelayMs * 2 ** Math.max(attempt - 1, 0), retryMaximumDelayMs)
  return Math.floor(Math.random() * maximum)
}

function abortableDelay(ms: number, signal?: AbortSignal): Promise<boolean> {
  if (isAborted(signal)) return Promise.resolve(false)
  return new Promise((resolve) => {
    const timeout = globalThis.setTimeout(() => {
      finish(true)
    }, ms)
    const onAbort = () => {
      finish(false)
    }
    function finish(completed: boolean) {
      globalThis.clearTimeout(timeout)
      signal?.removeEventListener('abort', onAbort)
      resolve(completed)
    }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

function linkedController(signal?: AbortSignal): {
  controller: AbortController
  unlink: () => void
} {
  const controller = new AbortController()
  const onAbort = () => {
    controller.abort(signal?.reason)
  }
  if (isAborted(signal)) onAbort()
  else signal?.addEventListener('abort', onAbort, { once: true })
  return {
    controller,
    unlink: () => {
      signal?.removeEventListener('abort', onAbort)
    },
  }
}

function notifyConnectionState(
  callback: OpenAgentEventStreamOptions['onConnectionStateChange'],
  state: AgentEventStreamConnectionState,
): void {
  try {
    callback?.(state)
  } catch (error) {
    throw streamError('client', 'Agent event stream connection callback failed', { cause: error })
  }
}

async function cancelResponseBody(response: Response | undefined): Promise<void> {
  if (response?.body == null || response.body.locked) return
  try {
    await response.body.cancel()
  } catch {
    // Cleanup is best effort after the owning reader has already been canceled.
  }
}

function headersForAttempt(
  headers: StreamEventsData['headers'],
  acknowledgedSequence: number | undefined,
): StreamEventsData['headers'] {
  if (acknowledgedSequence == null) return headers
  return { ...headers, 'Last-Event-ID': acknowledgedSequence }
}

export async function* openAgentEventStream({
  client,
  path,
  query,
  headers,
  signal,
  onConnectionStateChange,
}: OpenAgentEventStreamOptions): AsyncGenerator<AgentEventStreamFrame> {
  let acknowledgedSequence: number | undefined
  let consecutiveFailures = 0
  let hasRetried = false

  while (!isAborted(signal)) {
    const { controller, unlink } = linkedController(signal)
    let activeResponse: Response | undefined
    let connectedAt: number | undefined
    let connectionError: AgentEventStreamError | undefined
    let fetchInvoked = false
    let readTimedOut = false
    let yieldedApplicationFrame = false
    let readActive = false
    let readTimeout: ReturnType<typeof globalThis.setTimeout> | undefined
    let iterator: AsyncIterator<unknown> | undefined
    let failure: AgentEventStreamError | undefined
    let retryAfterMs: number | undefined

    const clearReadTimeout = () => {
      if (readTimeout != null) globalThis.clearTimeout(readTimeout)
      readTimeout = undefined
    }
    const hasReadTimedOut = () => readTimedOut
    const touchReadTimeout = () => {
      if (!readActive) return
      clearReadTimeout()
      readTimeout = globalThis.setTimeout(() => {
        readTimedOut = true
        controller.abort()
      }, activeReadTimeoutMs)
    }

    const configuredFetch = client.getConfig().fetch ?? globalThis.fetch
    const validatingFetch: typeof fetch = async (input, init) => {
      fetchInvoked = true
      const response = await configuredFetch(input, init)
      activeResponse = response
      if (response.status !== 200) {
        const apiError = await ApiError.fromResponse(response)
        retryAfterMs = parseRetryAfter(response.headers.get('Retry-After'))
        await cancelResponseBody(response)
        throw streamError('http', apiError.message, {
          status: response.status,
          code: apiError.code,
          cause: apiError,
        })
      }
      const mediaType = response.headers.get('Content-Type')?.split(';', 1)[0]?.trim().toLowerCase()
      if (mediaType !== 'text/event-stream') {
        await cancelResponseBody(response)
        throw streamError('contract', 'Agent event stream response is not text/event-stream')
      }
      if (response.body == null) {
        throw streamError('contract', 'Agent event stream response has no body')
      }
      if (isAborted(controller.signal)) {
        await cancelResponseBody(response)
        throw controller.signal.reason ?? new DOMException('Aborted', 'AbortError')
      }
      connectedAt = Date.now()
      notifyConnectionState(onConnectionStateChange, {
        state: 'connected',
        reconnected: hasRetried,
      })
      // A connection callback may synchronously abort after the pre-callback guard.
      // Check again before the generated reader installs its abort listener.
      if (isAborted(controller.signal)) {
        await cancelResponseBody(response)
        throw controller.signal.reason ?? new DOMException('Aborted', 'AbortError')
      }
      return response
    }

    try {
      const result = await streamEvents({
        client,
        path,
        query,
        headers: headersForAttempt(headers, acknowledgedSequence),
        signal: controller.signal,
        fetch: validatingFetch,
        responseValidator: undefined,
        sseMaxRetryAttempts: 1,
        onSseEvent: touchReadTimeout,
        onSseError: (error) => {
          connectionError = classifyAttemptError(error, { fetchInvoked, readTimedOut })
        },
      })
      iterator = result.stream[Symbol.asyncIterator]()

      while (!isAborted(signal)) {
        readActive = true
        touchReadTimeout()
        let next: IteratorResult<unknown>
        try {
          next = await iterator.next()
        } finally {
          readActive = false
          clearReadTimeout()
        }
        if (next.done) {
          if (hasReadTimedOut()) {
            throw streamError('transport', 'Agent event stream timed out waiting for data')
          }
          throw connectionError ?? streamError('transport', 'Agent event stream ended unexpectedly')
        }

        const data = await validateFrame(next.value)
        if (isAborted(signal)) return
        if (isApiErrorBody(data)) {
          throw streamError('api', data.error, { code: data.code })
        }
        const sequence = durableSequence(data)
        if (sequence != null && acknowledgedSequence != null && sequence <= acknowledgedSequence) {
          continue
        }

        yield data
        yieldedApplicationFrame = true
        if (sequence != null) acknowledgedSequence = sequence
      }
    } catch (error) {
      if (!isAborted(signal)) {
        failure = classifyAttemptError(error, { fetchInvoked, readTimedOut })
      }
    } finally {
      readActive = false
      clearReadTimeout()
      controller.abort()
      try {
        await iterator?.return?.()
      } catch {
        // The original failure is authoritative; abort already canceled the reader.
      }
      // Validation and connected-callback failures happen before the generated
      // reader owns the body, so the controller/iterator cleanup cannot close it.
      await cancelResponseBody(activeResponse)
      unlink()
    }

    if (isAborted(signal)) return
    if (failure == null) return
    if (!retryable(failure)) throw failure

    if (
      yieldedApplicationFrame ||
      (connectedAt != null && Date.now() - connectedAt >= stableConnectionResetMs)
    ) {
      consecutiveFailures = 0
    }
    consecutiveFailures += 1
    const delayMs = retryAfterMs ?? reconnectDelay(consecutiveFailures)
    notifyConnectionState(onConnectionStateChange, {
      state: 'reconnecting',
      attempt: consecutiveFailures,
      delayMs,
      error: failure,
    })
    hasRetried = true
    if (!(await abortableDelay(delayMs, signal))) return
  }
}
