import * as z from 'zod'

import type { OmnaraClient } from './client'
import { ApiError, type ApiErrorCode } from './errors'
import type {
  AgentEventStreamData,
  Error as ApiErrorBody,
  StreamEventsData,
} from './generated/types.gen'
import { zAgentSequence, zError, zStreamEventsResponse } from './generated/zod.gen'
import { createServerSentEventParser } from './server-sent-events'
import { schemaMismatch } from './validate-response'

export type AgentEventStreamFrame = Exclude<AgentEventStreamData, ApiErrorBody>
export type AgentEventStreamErrorKind = 'api' | 'client' | 'contract' | 'http' | 'transport'

export class AgentEventStreamError extends Error {
  readonly kind: AgentEventStreamErrorKind
  readonly status: number | undefined
  readonly code: ApiErrorCode | undefined
  readonly retryAfterMs: number | undefined

  constructor({
    kind,
    message,
    status,
    code,
    retryAfterMs,
    cause,
  }: {
    kind: AgentEventStreamErrorKind
    message: string
    status?: number
    code?: ApiErrorCode
    retryAfterMs?: number
    cause?: unknown
  }) {
    super(message, { cause })
    this.name = 'AgentEventStreamError'
    this.kind = kind
    this.status = status
    this.code = code
    this.retryAfterMs = retryAfterMs
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

const streamUrl: StreamEventsData['url'] =
  '/orgs/{orgID}/projects/{projectID}/agents/{agentID}/events/stream'
const retryBaseDelayMs = 1_000
const retryMaximumDelayMs = 30_000
const retryAfterMaximumDelayMs = 60_000
const stableConnectionResetMs = 10_000
// Must stay above the server heartbeat interval (agentEventStreamHeartbeatInterval in
// internal/httpapi/agent_runtime_routes.go); a quieter server would look stalled.
const stallTimeoutMs = 35_000

// event_kind stays open so kinds newer than this SDK still advance the cursor.
const zDurableCursor = z.object({
  event_kind: z.string(),
  sequence: zAgentSequence,
})

function streamError(
  kind: AgentEventStreamErrorKind,
  message: string,
  options: { status?: number; code?: ApiErrorCode; retryAfterMs?: number; cause?: unknown } = {},
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
  if (signal?.aborted === true) return Promise.resolve(false)
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

type ConnectionEvent = { kind: 'connected' } | { kind: 'message'; data: string }
type ConnectionOptions = Pick<OpenAgentEventStreamOptions, 'path' | 'query' | 'headers'>

async function settleWithin<T>(
  pending: Promise<T>,
  ms: number,
  attempt: AbortController,
): Promise<T> {
  let timer: ReturnType<typeof globalThis.setTimeout> | undefined
  const deadline = new Promise<never>((_, reject) => {
    timer = globalThis.setTimeout(() => {
      const error = streamError('transport', 'Agent event stream stalled')
      attempt.abort(error)
      reject(error)
    }, ms)
  })
  try {
    return await Promise.race([pending, deadline])
  } finally {
    globalThis.clearTimeout(timer)
  }
}

async function* openConnection(
  client: OmnaraClient,
  { path, query, headers }: ConnectionOptions,
  callerSignal: AbortSignal | undefined,
): AsyncGenerator<ConnectionEvent, never> {
  const attempt = new AbortController()
  const signal =
    callerSignal == null ? attempt.signal : AbortSignal.any([callerSignal, attempt.signal])
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined
  try {
    const dispatch = { started: false }
    const { error, response }: { error?: unknown; response?: Response } = await settleWithin(
      client.request({
        method: 'GET',
        url: streamUrl,
        path,
        query,
        headers: { Accept: 'text/event-stream', ...headers },
        parseAs: 'stream',
        signal,
        throwOnError: false,
        fetch: (input, init) => {
          dispatch.started = true
          return (client.getConfig().fetch ?? globalThis.fetch)(input, init)
        },
      }),
      stallTimeoutMs,
      attempt,
    )
    if (response == null) {
      throw dispatch.started
        ? streamError('transport', 'Agent event stream disconnected', { cause: error })
        : streamError('client', 'Agent event stream request could not be prepared', {
            cause: error,
          })
    }
    if (response.status !== 200) {
      const apiError = error instanceof ApiError ? error : ApiError.fromBody(response.status, error)
      await response.body?.cancel().catch(() => undefined)
      throw streamError('http', apiError.message, {
        status: response.status,
        code: apiError.code,
        retryAfterMs: parseRetryAfter(response.headers.get('Retry-After')),
        cause: apiError,
      })
    }
    if (error !== undefined) {
      throw streamError('client', 'Agent event stream response was rejected', { cause: error })
    }
    const mediaType = response.headers.get('Content-Type')?.split(';', 1)[0]?.trim().toLowerCase()
    if (mediaType !== 'text/event-stream') {
      await response.body?.cancel().catch(() => undefined)
      throw streamError('contract', 'Agent event stream response is not text/event-stream')
    }
    if (response.body == null) {
      throw streamError('contract', 'Agent event stream response has no body')
    }
    reader = response.body.getReader()
    signal.throwIfAborted()
    yield { kind: 'connected' }
    const parser = createServerSentEventParser()
    const decoder = new TextDecoder()
    for (;;) {
      signal.throwIfAborted()
      const chunk = await settleWithin(reader.read(), stallTimeoutMs, attempt)
      if (chunk.done) throw streamError('transport', 'Agent event stream ended unexpectedly')
      for (const data of parser.push(decoder.decode(chunk.value, { stream: true }))) {
        yield { kind: 'message', data }
      }
    }
  } catch (error) {
    throw error instanceof AgentEventStreamError
      ? error
      : streamError('transport', 'Agent event stream disconnected', { cause: error })
  } finally {
    await reader?.cancel().catch(() => undefined)
  }
}

async function decodeFrame(text: string): Promise<AgentEventStreamData> {
  let data: unknown
  try {
    data = JSON.parse(text)
  } catch (error) {
    throw streamError('contract', 'Agent event stream received data that is not JSON', {
      cause: error,
    })
  }
  const mismatch = await schemaMismatch(zStreamEventsResponse, data)
  if (mismatch != null) {
    throw streamError('contract', 'Agent event stream received data outside its API contract', {
      cause: mismatch,
    })
  }
  return data as AgentEventStreamData
}

function durableSequence(data: AgentEventStreamData): number | undefined {
  const cursor = zDurableCursor.safeParse(data)
  return cursor.success ? cursor.data.sequence : undefined
}

export async function* openAgentEventStream({
  client,
  path,
  query,
  headers,
  signal,
  onConnectionStateChange,
}: OpenAgentEventStreamOptions): AsyncGenerator<AgentEventStreamFrame> {
  let cursor: number | undefined
  let consecutiveFailures = 0
  let hasRetried = false

  while (true) {
    let connectedAt: number | undefined
    try {
      const connection = openConnection(
        client,
        {
          path,
          query,
          headers: cursor == null ? headers : { ...headers, 'Last-Event-ID': cursor },
        },
        signal,
      )
      for await (const event of connection) {
        if (event.kind === 'connected') {
          connectedAt = Date.now()
          notifyConnectionState(onConnectionStateChange, {
            state: 'connected',
            reconnected: hasRetried,
          })
          continue
        }
        const data = await decodeFrame(event.data)
        signal?.throwIfAborted()
        const sequence = durableSequence(data)
        if (sequence == null && (await schemaMismatch(zError, data)) == null) {
          const body = data as ApiErrorBody
          throw streamError('api', body.error, { code: body.code })
        }
        if (sequence != null && cursor != null && sequence <= cursor) continue
        yield data as AgentEventStreamFrame
        consecutiveFailures = 0
        if (sequence != null) cursor = sequence
      }
    } catch (error) {
      if (signal?.aborted === true) return
      if (!(error instanceof AgentEventStreamError) || !retryable(error)) throw error
      if (connectedAt != null && Date.now() - connectedAt >= stableConnectionResetMs) {
        consecutiveFailures = 0
      }
      consecutiveFailures += 1
      const delayMs = Math.max(reconnectDelay(consecutiveFailures), error.retryAfterMs ?? 0)
      notifyConnectionState(onConnectionStateChange, {
        state: 'reconnecting',
        attempt: consecutiveFailures,
        delayMs,
        error,
      })
      hasRetried = true
      if (!(await abortableDelay(delayMs, signal))) return
    }
  }
}
