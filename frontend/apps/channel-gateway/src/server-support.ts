import { type IncomingMessage, validateHeaderValue } from 'node:http'

import type { ProviderWebhookWorkContext, ProviderWorkReservation } from './types'
import { GatewayAtCapacityError, WorkReservationScope } from './work-budget'

export class BackgroundTaskTracker implements ProviderWebhookWorkContext {
  private closed = false
  private readonly tasks: Promise<{ error?: unknown }>[] = []
  private readonly workReservations: WorkReservationScope

  constructor(reserve: (bytes: number) => ProviderWorkReservation) {
    this.workReservations = new WorkReservationScope(reserve)
  }

  reserveWorkBytes = (bytes: number): ProviderWorkReservation => {
    if (this.closed) throw new Error('provider webhook context is closed')
    return this.workReservations.reserve(bytes)
  }

  waitUntil = (task: Promise<unknown>): void => {
    if (this.closed) {
      void task.catch(() => undefined)
      return
    }
    this.tasks.push(
      task.then(
        () => ({}),
        (error: unknown) => ({ error }),
      ),
    )
  }

  async drain(signal: AbortSignal): Promise<unknown[]> {
    const failures: unknown[] = []
    let cursor = 0
    try {
      while (cursor < this.tasks.length) {
        const batch = this.tasks.slice(cursor)
        cursor = this.tasks.length
        const results = await raceWithAbort(Promise.all(batch), signal)
        for (const result of results) {
          if ('error' in result) failures.push(result.error)
        }
        await Promise.resolve()
      }
      return failures
    } finally {
      this.close()
    }
  }

  close(): void {
    if (this.closed) return
    this.closed = true
    this.workReservations.close()
  }
}

export class BodyTooLargeError extends Error {}

class ProviderResponseTooLargeError extends Error {
  constructor() {
    super('provider webhook response body is too large')
  }
}

const hopByHopResponseHeaders = new Set([
  'connection',
  'keep-alive',
  'proxy-authenticate',
  'proxy-authorization',
  'te',
  'trailer',
  'transfer-encoding',
  'upgrade',
])

export function providerResponseHeaders(headers: Headers): Headers {
  const blocked = new Set(hopByHopResponseHeaders)
  for (const name of headers.get('connection')?.split(',') ?? []) {
    const normalized = name.trim().toLowerCase()
    if (normalized) blocked.add(normalized)
  }
  // Node frames the buffered body itself. Forwarding the provider's original
  // length or transfer metadata could truncate or hang the response.
  blocked.add('content-length')
  // Provider headers must not tell the Node adapter to skip writing a response.
  blocked.add('x-hono-already-sent')

  const forwarded = new Headers(headers)
  // Fetch permits control characters that Node's response writer rejects.
  // Validate before handing off so failures use the gateway's error handler.
  for (const [name, value] of headers) {
    if (blocked.has(name)) forwarded.delete(name)
    else validateHeaderValue(name, value)
  }
  return forwarded
}

export async function raceWithAbort<T>(work: Promise<T>, signal: AbortSignal): Promise<T> {
  if (signal.aborted) throw abortError(signal)
  return new Promise<T>((resolve, reject) => {
    const onAbort = (): void => {
      reject(abortError(signal))
    }
    signal.addEventListener('abort', onAbort, { once: true })
    work.then(
      (value) => {
        signal.removeEventListener('abort', onAbort)
        resolve(value)
      },
      (error: unknown) => {
        signal.removeEventListener('abort', onAbort)
        reject(error instanceof Error ? error : new Error(String(error)))
      },
    )
  })
}

export function abortError(signal: AbortSignal): Error {
  return signal.reason instanceof Error
    ? signal.reason
    : new Error('channel webhook request was aborted')
}

export interface BufferedBody {
  body: Buffer
  release: () => void
}

export async function readBody(
  request: IncomingMessage,
  limit: number,
  signal: AbortSignal,
  adjustReservation: (bytes: number) => boolean,
): Promise<BufferedBody> {
  return new Promise<BufferedBody>((resolve, reject) => {
    const chunks: Buffer[] = []
    let reserved = 0
    let size = 0
    let settled = false
    let released = false
    const cleanup = (): void => {
      request.removeListener('aborted', onAborted)
      request.removeListener('data', onData)
      request.removeListener('end', onEnd)
      request.removeListener('error', onError)
      signal.removeEventListener('abort', onSignalAbort)
    }
    const returnReservation = (): void => {
      if (reserved === 0) return
      adjustReservation(-reserved)
      reserved = 0
    }
    const releaseReservation = (): void => {
      if (released) return
      released = true
      returnReservation()
    }
    const fail = (error: Error): void => {
      if (settled) return
      settled = true
      cleanup()
      returnReservation()
      request.pause()
      reject(error)
    }
    const onAborted = (): void => {
      fail(new Error('request body was aborted'))
    }
    const onSignalAbort = (): void => {
      fail(abortError(signal))
    }
    const onData = (chunk: Buffer | string): void => {
      // Incoming Buffer chunks are already exclusively retained by this
      // request. Re-wrapping them would create an unaccounted full-size copy.
      const buffer = typeof chunk === 'string' ? Buffer.from(chunk) : chunk
      size += buffer.byteLength
      if (size > limit) {
        chunks.length = 0
        fail(new BodyTooLargeError())
        return
      }
      if (!adjustReservation(buffer.byteLength)) {
        chunks.length = 0
        fail(new GatewayAtCapacityError())
        return
      }
      reserved += buffer.byteLength
      chunks.push(buffer)
    }
    const onEnd = (): void => {
      if (settled) return
      if (chunks.length === 1 && chunks[0] !== undefined) {
        settled = true
        cleanup()
        resolve({ body: chunks[0], release: releaseReservation })
        return
      }
      // Buffer.concat allocates a second full body while the chunks are still
      // retained. Reserve that transient copy before allocating it, then release
      // the chunk reservation once only the contiguous body remains.
      if (size > 0 && !adjustReservation(size)) {
        chunks.length = 0
        fail(new GatewayAtCapacityError())
        return
      }
      reserved += size
      let body: Buffer
      try {
        body = Buffer.concat(chunks, size)
      } catch (error) {
        chunks.length = 0
        fail(error instanceof Error ? error : new Error(String(error)))
        return
      }
      chunks.length = 0
      adjustReservation(-size)
      reserved -= size
      settled = true
      cleanup()
      resolve({ body, release: releaseReservation })
    }
    const onError = (error: Error): void => {
      fail(error)
    }
    if (signal.aborted) {
      fail(abortError(signal))
      return
    }
    request.on('aborted', onAborted)
    request.on('data', onData)
    request.on('end', onEnd)
    request.on('error', onError)
    signal.addEventListener('abort', onSignalAbort, { once: true })
  })
}

export function declaredBodyExceedsLimit(request: IncomingMessage, limit: number): boolean {
  const value = request.headers['content-length']?.trim()
  if (!value || !/^\d+$/.test(value)) return false
  return BigInt(value) > BigInt(limit)
}

export async function readProviderResponseBody(
  response: Response,
  limit: number,
  signal: AbortSignal,
): Promise<Buffer<ArrayBuffer>> {
  const declaredLength = response.headers.get('content-length')?.trim()
  if (declaredLength && /^\d+$/.test(declaredLength) && BigInt(declaredLength) > BigInt(limit)) {
    throw new ProviderResponseTooLargeError()
  }
  if (!response.body) return Buffer.alloc(0)

  const reader = response.body.getReader()
  const chunks: Buffer[] = []
  let size = 0
  let completed = false
  try {
    while (true) {
      const result = await raceWithAbort(reader.read(), signal)
      if (result.done) {
        completed = true
        return Buffer.concat(chunks, size)
      }
      size += result.value.byteLength
      if (size > limit) throw new ProviderResponseTooLargeError()
      chunks.push(Buffer.from(result.value))
    }
  } finally {
    if (completed) {
      reader.releaseLock()
    } else {
      void reader
        .cancel(signal.reason)
        .then(() => {
          reader.releaseLock()
        })
        .catch(() => undefined)
    }
  }
}
