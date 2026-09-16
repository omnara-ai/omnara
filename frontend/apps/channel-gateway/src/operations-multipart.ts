import type { IncomingMessage } from 'node:http'
import { PassThrough, type Readable } from 'node:stream'
import { MIMEType } from 'node:util'

import { Dicer } from '@fastify/busboy'

import { raceWithAbort } from './async'
import { isString } from './diagnostics'
import type { OperationArtifact, OperationFiles } from './operations-files'
import {
  type GatewayOperation,
  InvalidOperationError,
  maxOperationArtifacts,
  maxOperationEnvelopeBytes,
} from './operations-json'

export interface ParsedOperationBody {
  operation: GatewayOperation
  artifacts: OperationArtifact[]
}

export function multipartBoundary(contentType: string): string | undefined {
  const type = strictMIMEType(contentType)
  if (type.essence === 'application/json') {
    if (
      [...type.params].some(([key, value]) => key !== 'charset' || value.toLowerCase() !== 'utf-8')
    ) {
      throw new InvalidOperationError()
    }
    return undefined
  }
  const boundary = type.params.get('boundary')
  if (
    type.essence !== 'multipart/form-data' ||
    [...type.params].length !== 1 ||
    !boundary ||
    !/^[0-9A-Za-z'()+_,./:=? -]{1,70}$/.test(boundary) ||
    boundary.endsWith(' ')
  ) {
    throw new InvalidOperationError()
  }
  return boundary
}

/** Dicer is the public streaming export of @fastify/busboy@3.2.2.
 * Unlike its form convenience layer, every part is visible for validation.
 * MIME syntax follows this parser's tolerant semantics: an invalid trailing
 * header line can terminate its header map. Only the selected name/type/file
 * enter our contract, checked against the envelope; discarded header text
 * cannot create a part, change scope, or bypass actual byte budgets. Providers
 * receive bounded scratch files, never the original multipart for reparsing.
 * Source: https://github.com/fastify/busboy/blob/v3.2.2/deps/dicer/lib/Dicer.js
 */
export async function readOperationMultipart(
  incoming: IncomingMessage,
  boundary: string,
  maxRequestBytes: number,
  files: OperationFiles,
  parentSignal: AbortSignal,
  acceptEnvelope: (raw: string) => GatewayOperation,
): Promise<ParsedOperationBody> {
  const canceled = new AbortController()
  const signal = AbortSignal.any([parentSignal, canceled.signal])
  // Extra tuning fields are supported by the pinned implementation, but absent
  // from its bundled Dicer.Config declaration; keep them visible in this object.
  const parserOptions = {
    boundary,
    maxHeaderPairs: 4,
    maxHeaderSize: 8 * 1024,
    partHwm: 32 * 1024,
    highWaterMark: 32 * 1024,
  }
  const parser = new Dicer(parserOptions)
  const intake = new PassThrough({ highWaterMark: 32 * 1024 })
  const streams = new Set<Readable>()
  let operation: GatewayOperation | undefined
  const artifacts: OperationArtifact[] = []
  let parts = 0
  let readingHeaders = false
  let headerWorkBytes = 0
  const headersPending = (): boolean => readingHeaders
  let failed: Error | undefined
  let consume = Promise.resolve()
  const stop = (error: Error): void => {
    failed ??= error
    canceled.abort()
    incoming.unpipe(intake)
    incoming.pause()
    intake.destroy(error)
    parser.destroy(error)
    for (const stream of streams) stream.destroy(error)
  }
  const onAbort = (): void => {
    stop(new InvalidOperationError())
  }
  signal.addEventListener('abort', onAbort, { once: true })
  const finished = new Promise<void>((resolve, reject) => {
    parser.on('finish', resolve)
    parser.on('error', () => {
      const error = new InvalidOperationError()
      stop(error)
      reject(failed ?? error)
    })
    parser.once('close', () => {
      if (failed) reject(failed)
    })
  })
  // Observe before awaiting intake; a parser can fail while file I/O is pending.
  void finished.catch(() => undefined)
  parser.on('preamble', (part) => {
    part.on('error', () => undefined)
    part.resume() // RFC preamble/epilogue are ignored but count toward total bytes.
  })
  parser.on('part', (part) => {
    readingHeaders = true
    headerWorkBytes = 0
    streams.add(part)
    part.on('error', () => undefined)
    const index = parts++
    if (parts > maxOperationArtifacts + 1) {
      stop(new InvalidOperationError())
      return
    }
    let partHeaders: object | undefined
    part.once('header', (headers) => {
      readingHeaders = false
      headerWorkBytes = 0
      partHeaders = headers
    })
    consume = consume
      .then(async () => {
        if (failed) throw failed
        if (!partHeaders)
          await raceWithAbort(
            new Promise<void>((resolve) => {
              part.once('header', () => {
                resolve()
              })
            }),
            signal,
          )
        const headers = parsePartHeaders(partHeaders)
        if (index === 0) {
          if (
            headers.name !== 'operation' ||
            headers.filename !== undefined ||
            headers.contentType !== 'application/json'
          ) {
            throw new InvalidOperationError()
          }
          operation = acceptEnvelope(await readStreamText(part, maxOperationEnvelopeBytes, signal))
        } else {
          const metadata = operation?.artifacts[index - 1]
          if (
            !metadata ||
            headers.name !== 'artifact' ||
            headers.filename !== metadata.filename ||
            headers.contentType !== metadata.content_type
          )
            throw new InvalidOperationError()
          artifacts.push(await files.save(part, metadata, index))
        }
        streams.delete(part)
      })
      .catch((cause: unknown) => {
        stop(cause instanceof Error ? cause : new InvalidOperationError())
      })
  })
  const onIncomingError = (): void => {
    stop(new InvalidOperationError())
  }
  incoming.once('error', onIncomingError)
  incoming.once('aborted', onIncomingError)
  incoming.pipe(intake)
  try {
    let bytes = 0
    for await (const chunk of intake) {
      if (!Buffer.isBuffer(chunk)) throw new InvalidOperationError()
      bytes += chunk.length
      if (bytes > maxRequestBytes) throw new InvalidOperationError()
      if (failed) throw failed
      // Dicer bounds retained headers by truncating, not rejecting. Meter its
      // public header phase too. Small writes bound overshoot without parsing
      // delimiters ourselves; valid Go headers fit within one 4 KiB segment.
      for (let offset = 0; offset < chunk.length; offset += 4096) {
        const segment = chunk.subarray(offset, offset + 4096)
        const ready = parser.write(segment)
        if (headersPending()) {
          headerWorkBytes += segment.length
          if (headerWorkBytes >= 8192) throw new InvalidOperationError()
        }
        if (ready) continue
        signal.throwIfAborted()
        await raceWithAbort(
          new Promise<void>((resolve, reject) => {
            const drained = (): void => {
              parser.removeListener('error', rejected)
              resolve()
            }
            const rejected = (): void => {
              parser.removeListener('drain', drained)
              reject(new InvalidOperationError())
            }
            parser.once('drain', drained)
            parser.once('error', rejected)
          }),
          signal,
        )
      }
    }
    parser.end()
    // Dicer can close its writable side before all part consumers validate.
    // A later rejection must cancel this wait even when no further parser
    // error/finish event can be emitted by an already destroyed stream.
    await raceWithAbort(finished, signal)
    await consume
    if (failed) throw failed
    if (
      !operation ||
      artifacts.length !== operation.artifacts.length ||
      parts !== artifacts.length + 1
    ) {
      throw new InvalidOperationError()
    }
    return { operation, artifacts }
  } catch (cause) {
    // Internal cancellation wakes pending parser/drain waits. Preserve the
    // original intake failure, including temporary capacity, over that wakeup.
    throw failed ?? cause
  } finally {
    stop(new InvalidOperationError())
    signal.removeEventListener('abort', onAbort)
    incoming.removeListener('error', onIncomingError)
    incoming.removeListener('aborted', onIncomingError)
    await consume
  }
}

export async function readStreamText(
  stream: Readable,
  limit: number,
  signal: AbortSignal,
): Promise<string> {
  const chunks: Buffer[] = []
  let bytes = 0
  const stop = (): void => {
    stream.destroy(new InvalidOperationError())
  }
  signal.addEventListener('abort', stop, { once: true })
  try {
    signal.throwIfAborted()
    for await (const chunk of stream) {
      if (!Buffer.isBuffer(chunk)) throw new InvalidOperationError()
      bytes += chunk.length
      if (bytes > limit) throw new InvalidOperationError()
      chunks.push(chunk)
    }
    return new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(chunks, bytes))
  } finally {
    signal.removeEventListener('abort', stop)
  }
}

function parsePartHeaders(cause: unknown) {
  if (!isPartHeaders(cause)) throw new InvalidOperationError()
  const entries = Object.entries(cause)
  if (
    entries.some(
      ([_key, values]) => values.length !== 1 || Buffer.byteLength(values[0] ?? '') > 1024,
    )
  )
    throw new InvalidOperationError()
  const dispositionRaw = cause['content-disposition']?.[0]
  const contentTypeRaw = cause['content-type']?.[0]
  if (!dispositionRaw || !contentTypeRaw) throw new InvalidOperationError()
  const disposition = strictMIMEType(`application/${dispositionRaw}`)
  if (disposition.essence !== 'application/form-data') throw new InvalidOperationError()
  const name = disposition.params.get('name')
  let filename = disposition.params.get('filename') ?? undefined
  const extended = disposition.params.get('filename*')
  if (extended !== null) {
    if (filename !== undefined || !extended.startsWith("utf-8''")) throw new InvalidOperationError()
    filename = decodeURIComponent(extended.slice(7))
  }
  if ([...disposition.params].some(([key]) => !['name', 'filename', 'filename*'].includes(key))) {
    throw new InvalidOperationError()
  }
  const type = strictMIMEType(contentTypeRaw)
  if ([...type.params].length !== 0) throw new InvalidOperationError()
  return { name, filename, contentType: type.essence }
}

function isPartHeaders(value: unknown): value is Record<string, string[]> {
  return value !== null && typeof value === 'object' && Object.values(value).every(isHeaderValues)
}

function isHeaderValues(value: unknown): value is string[] {
  return Array.isArray(value) && value.every(isString)
}

// MIMEType deliberately ignores duplicate/malformed parameters. Check original
// parameter names as well so two parsers cannot select different names or files.
function strictMIMEType(raw: string): MIMEType {
  const type = new MIMEType(raw)
  const segments: string[] = []
  let start = 0
  let quoted = false
  for (let i = 0; i < raw.length; i += 1) {
    if (quoted && raw[i] === '\\') {
      i += 1
      continue
    }
    if (raw[i] === '"') quoted = !quoted
    if (!quoted && raw[i] === ';') {
      segments.push(raw.slice(start, i))
      start = i + 1
    }
  }
  if (quoted) throw new InvalidOperationError()
  segments.push(raw.slice(start))
  const keys = new Set<string>()
  for (const segment of segments.slice(1)) {
    const equals = segment.indexOf('=')
    const key = segment.slice(0, equals).trim().toLowerCase()
    if (equals < 1 || keys.has(key) || type.params.get(key) === null)
      throw new InvalidOperationError()
    keys.add(key)
  }
  return type
}
