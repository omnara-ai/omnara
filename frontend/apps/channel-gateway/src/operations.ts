import { isUtf8 } from 'node:buffer'
import { createHash, timingSafeEqual } from 'node:crypto'
import type { IncomingMessage, ServerResponse } from 'node:http'
import { PassThrough } from 'node:stream'

import {
  type ChannelConnectorCapability,
  type ChannelOperationFailure,
  type JsonBody,
  schemas,
} from '@omnara/sdk'

import { raceWithAbort } from './async'
import { OperationRetryError } from './operation-retry'
import {
  type OperationArtifact,
  OperationFiles,
  TemporaryStorageFullError,
} from './operations-files'
import {
  capabilityKey,
  type GatewayOperation,
  InvalidOperationError,
  isReadOnlyOperation,
  maxOperationEnvelopeBytes,
  maxOperationResponseBytes,
  parseObjectFields,
  parseOperation,
  serializeOperationResult,
} from './operations-json'
import { multipartBoundary, readOperationMultipart, readStreamText } from './operations-multipart'
import { declaredBodyExceedsLimit } from './server-support'
import type { GatewayLogger } from './types'
import { GatewayAtCapacityError, WorkByteBudget } from './work-budget'

export type { OperationArtifact } from './operations-files'
export type {
  GatewayOperation,
  InstallationOperationScope,
  OperationArtifactMetadata,
  OperationKind,
  OperationScope,
} from './operations-json'
export {
  maxOperationArtifacts,
  maxOperationEnvelopeBytes,
  maxOperationPayloadBytes,
  maxOperationResponseBytes,
} from './operations-json'

export const operationsRoute = '/internal/operations'
// Unpadded base64url of the existing UTF-8 request ID (at most 256 bytes).
export const operationRequestIdHeader = 'X-Omnara-Channel-Request-ID'
// Includes bounded JSON text/tree/serialization headroom and parser/file stream
// buffers. JSON container count/depth are bounded independently by the decoder.
export const operationWorkBytes = 16 * 1024 * 1024

export type OperationExecutionResult =
  | { outcome: 'completed'; payload: JsonBody }
  | { outcome: 'failed' | 'unknown'; payload?: ChannelOperationFailure }

const failureSchema = schemas.zChannelOperationFailure.strict()

export interface OperationsOptions {
  credential: string
  allowedCapabilities: readonly ChannelConnectorCapability[]
  /** Shared with the rest of the gateway's retained work. */
  workBudget: WorkByteBudget
  logger?: Pick<GatewayLogger, 'error'>
  maxConcurrentRequests: number
  /** Aggregate actual scratch-file bytes across requests, owned by deployment. */
  maxTemporaryBytes: number
  /** Actual bytes of one finite HTTP request, including multipart overhead.
   * This operational budget is not a product per-file upload/egress ceiling.
   */
  maxRequestBytes: number
  maxDurationMs: number
  /** Deployment-owned ephemeral storage parent; filenames never select paths. */
  temporaryDirectory: string
  /** Recheck live app/install/channel authority here before provider mutation.
   * Parse payloadJSON against the operation schema; use retryOperation and carry
   * signal into real I/O. No ProviderRuntime/delivery compatibility mapping exists.
   */
  execute(
    operation: GatewayOperation,
    artifacts: readonly OperationArtifact[],
    signal: AbortSignal,
  ): Promise<OperationExecutionResult>
}

export class OperationsHandler {
  private readonly credentialHash: Buffer
  private readonly capabilities = new Set<string>()
  private readonly temporaryBudget: WorkByteBudget
  private readonly requests = new Map<AbortController, Promise<void>>()
  private slots = 0
  private closed = false

  constructor(private readonly options: OperationsOptions) {
    if (
      !options.credential ||
      options.credential.trim() !== options.credential ||
      options.credential.length > 1024 ||
      /\s/.test(options.credential) ||
      !options.temporaryDirectory ||
      ![
        options.maxConcurrentRequests,
        options.maxTemporaryBytes,
        options.maxRequestBytes,
        options.maxDurationMs,
      ].every((value) => Number.isSafeInteger(value) && value > 0) ||
      options.maxDurationMs > 2_147_483_647 ||
      options.allowedCapabilities.length === 0 ||
      options.allowedCapabilities.length > 64
    ) {
      throw new InvalidOperationError()
    }
    this.credentialHash = createHash('sha256').update(options.credential).digest()
    this.temporaryBudget = new WorkByteBudget(options.maxTemporaryBytes)
    for (const capability of options.allowedCapabilities) {
      const key = capabilityKey(capability)
      if (this.capabilities.has(key)) throw new InvalidOperationError()
      this.capabilities.add(key)
    }
  }

  /** Stop admission and allow active requests to finish within the grace period.
   * A callback ignoring cancellation keeps its admission reservation until it
   * settles; shutdown never waits indefinitely for uncooperative provider code.
   */
  async close(graceMs = 0): Promise<void> {
    this.closed = true
    const finished = Promise.allSettled(this.requests.values())
    let timer: ReturnType<typeof setTimeout> | undefined
    try {
      if (graceMs > 0) {
        await Promise.race([
          finished,
          new Promise<void>((resolve) => {
            timer = setTimeout(resolve, graceMs)
          }),
        ])
      }
      for (const controller of this.requests.keys()) controller.abort()
      await finished
    } finally {
      if (timer) clearTimeout(timer)
    }
  }

  async handle(incoming: IncomingMessage, outgoing: ServerResponse): Promise<Response> {
    if (!this.authenticated(incoming)) return rejection(401, 'unauthorized')
    let headerRequestId: string | undefined
    try {
      headerRequestId = requestIdFromHeader(incoming)
    } catch {
      return rejection(400, 'invalid_operation')
    }
    if (this.closed || this.slots >= this.options.maxConcurrentRequests)
      return rejection(503, 'at_capacity', headerRequestId)
    let work
    try {
      work = this.options.workBudget.reserve(operationWorkBytes)
    } catch {
      return rejection(503, 'at_capacity', headerRequestId)
    }
    this.slots += 1
    const controller = new AbortController()
    const { signal } = controller
    let finish!: () => void
    const finished = new Promise<void>((resolve) => {
      finish = resolve
    })
    this.requests.set(controller, finished)
    const ceilingMs = Date.now() + this.options.maxDurationMs
    let timer = setTimeout(() => {
      controller.abort()
    }, this.options.maxDurationMs)
    const disconnect = (): void => {
      if (!outgoing.writableFinished) controller.abort()
    }
    outgoing.once('close', disconnect)
    incoming.once('aborted', disconnect)
    const files = new OperationFiles(this.options.temporaryDirectory, this.temporaryBudget, signal)
    let operation: GatewayOperation | undefined
    const executionState = { started: false }
    let executionSettled = true
    let requestFinished = false
    const release = (): void => {
      work.release()
      this.slots -= 1
    }
    const acceptEnvelope = (raw: string): GatewayOperation => {
      const parsed = parseOperation(raw, this.capabilities, ceilingMs)
      if (headerRequestId !== undefined && parsed.requestId !== headerRequestId)
        throw new InvalidOperationError()
      operation = parsed
      clearTimeout(timer)
      const remaining = operation.deadlineMs - Date.now()
      if (remaining <= 0) {
        controller.abort()
        throw new InvalidOperationError()
      }
      timer = setTimeout(() => {
        controller.abort()
      }, remaining)
      return operation
    }
    try {
      if (declaredBodyExceedsLimit(incoming, this.options.maxRequestBytes)) {
        return rejection(413, 'request_too_large', headerRequestId)
      }
      const boundary = multipartBoundary(incoming.headers['content-type'] ?? '')
      let artifacts: OperationArtifact[] = []
      if (boundary === undefined) {
        const intake = new PassThrough({ highWaterMark: 32 * 1024 })
        const onIncomingError = (): void => {
          intake.destroy(new InvalidOperationError())
        }
        incoming.once('error', onIncomingError)
        incoming.once('aborted', onIncomingError)
        incoming.pipe(intake)
        try {
          operation = acceptEnvelope(
            await readStreamText(
              intake,
              Math.min(this.options.maxRequestBytes, maxOperationEnvelopeBytes),
              signal,
            ),
          )
        } finally {
          incoming.unpipe(intake)
          incoming.pause()
          intake.destroy()
          incoming.removeListener('error', onIncomingError)
          incoming.removeListener('aborted', onIncomingError)
        }
        if (operation.artifacts.length !== 0) throw new InvalidOperationError()
      } else {
        const parsed = await readOperationMultipart(
          incoming,
          boundary,
          this.options.maxRequestBytes,
          files,
          signal,
          acceptEnvelope,
        )
        operation = parsed.operation
        artifacts = parsed.artifacts
      }
      signal.throwIfAborted()
      if (Date.now() >= operation.deadlineMs) throw new InvalidOperationError()
      executionSettled = false
      const accepted = operation
      // Install observation before invoking the callback (including sync throws).
      const execution = Promise.resolve()
        .then(() => {
          signal.throwIfAborted()
          executionState.started = true
          return this.options.execute(accepted, artifacts, signal)
        })
        .finally(() => {
          executionSettled = true
          if (requestFinished) release()
        })
      const result = await raceWithAbort(execution, signal)
      signal.throwIfAborted()
      if (Date.now() >= operation.deadlineMs) throw new InvalidOperationError()
      return completion(operation, result)
    } catch (cause) {
      if (operation && executionState.started) {
        const unknown =
          !isReadOnlyOperation(operation.kind) &&
          (!(cause instanceof OperationRetryError) || cause.outcomeUnknown)
        return completion(operation, { outcome: unknown ? 'unknown' : 'failed' })
      }
      // Parsing the envelope does not mean the upload finished. Reject intake
      // failures before dispatch so core need not wait for an unread body.
      const requestId = operation?.requestId ?? headerRequestId
      if (cause instanceof GatewayAtCapacityError || cause instanceof TemporaryStorageFullError) {
        return rejection(503, 'at_capacity', requestId)
      }
      return rejection(
        signal.aborted ? 408 : 400,
        signal.aborted ? 'deadline_exceeded' : 'invalid_operation',
        requestId,
      )
    } finally {
      clearTimeout(timer)
      controller.abort()
      outgoing.removeListener('close', disconnect)
      incoming.removeListener('aborted', disconnect)
      try {
        await files.close()
      } catch {
        // Keep disk charged and emit a fixed diagnostic without paths or causes.
        this.options.logger?.error('channel operation temporary file cleanup failed')
      }
      requestFinished = true
      if (executionSettled) release()
      this.requests.delete(controller)
      finish()
    }
  }

  private authenticated(incoming: IncomingMessage): boolean {
    let count = 0
    for (let i = 0; i < incoming.rawHeaders.length; i += 2) {
      if (incoming.rawHeaders[i]?.toLowerCase() === 'authorization') count += 1
    }
    const authorization = incoming.headers.authorization ?? ''
    const token = /^Bearer ([^\s,]+)$/.exec(authorization)?.[1] ?? ''
    const candidate = createHash('sha256').update(token).digest()
    const matches = timingSafeEqual(candidate, this.credentialHash)
    return count === 1 && matches
  }
}

function completion(operation: GatewayOperation, result: OperationExecutionResult): Response {
  if (!['completed', 'failed', 'unknown'].includes(result.outcome)) {
    throw new InvalidOperationError()
  }
  const outcome =
    result.outcome === 'unknown' && isReadOnlyOperation(operation.kind) ? 'failed' : result.outcome
  const diagnostic =
    result.outcome === 'failed' ? failureSchema.safeParse(result.payload).data : undefined
  const body = { request_id: operation.requestId, outcome }
  const payload = result.outcome === 'completed' ? result.payload : diagnostic
  const raw = serializeOperationResult(payload === undefined ? body : { ...body, payload })
  const fields = parseObjectFields(raw, maxOperationResponseBytes)
  if (result.outcome === 'completed')
    parseObjectFields(fields.get('payload') ?? '', maxOperationResponseBytes)
  return new Response(raw, {
    status: 200,
    headers: {
      'content-type': 'application/json',
      'cache-control': 'no-store',
      connection: 'close',
    },
  })
}

function rejection(status: number, code: string, requestId?: string): Response {
  return new Response(
    JSON.stringify(
      requestId === undefined ? { error: code } : { request_id: requestId, outcome: 'failed' },
    ),
    {
      status,
      headers: {
        'content-type': 'application/json',
        'cache-control': 'no-store',
        connection: 'close',
      },
    },
  )
}

function requestIdFromHeader(incoming: IncomingMessage): string | undefined {
  let encoded: string | undefined
  let count = 0
  for (let index = 0; index < incoming.rawHeaders.length; index += 2) {
    if (incoming.rawHeaders[index]?.toLowerCase() !== operationRequestIdHeader.toLowerCase())
      continue
    count += 1
    encoded = incoming.rawHeaders[index + 1]
  }
  // Headerless test/private callers can still use the body, but supply no proof
  // of correlation for an early rejection. Never reinterpret an invalid header.
  if (count === 0) return undefined
  if (count !== 1 || !encoded || encoded.length > 342 || !/^[A-Za-z0-9_-]+$/.test(encoded))
    throw new InvalidOperationError()
  const bytes = Buffer.from(encoded, 'base64url')
  if (bytes.length > 256 || !isUtf8(bytes) || bytes.toString('base64url') !== encoded)
    throw new InvalidOperationError()
  const requestId = bytes.toString('utf8')
  // Same text domain as Go validOperationText and the operation JSON parser.
  if (
    !requestId ||
    requestId.trim() !== requestId ||
    ['\u0000', '\r', '\n'].some((character) => requestId.includes(character))
  )
    throw new InvalidOperationError()
  return requestId
}
