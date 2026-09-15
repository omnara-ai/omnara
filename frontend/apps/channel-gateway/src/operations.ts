import { createHash, timingSafeEqual } from 'node:crypto'
import type { IncomingMessage, ServerResponse } from 'node:http'
import { PassThrough } from 'node:stream'

import type { ChannelConnectorCapability, JsonBody } from '@omnara/sdk'

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
// Includes bounded JSON text/tree/serialization headroom and parser/file stream
// buffers. JSON container count/depth are bounded independently by the decoder.
export const operationWorkBytes = 16 * 1024 * 1024

export type OperationExecutionResult =
  | { outcome: 'completed'; payload: JsonBody }
  | { outcome: 'failed' | 'unknown' }

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

  /** Abort immediately on shutdown, then await request/file cleanup only.
   * A callback ignoring cancellation keeps its admission reservation until it
   * settles; shutdown never waits indefinitely for uncooperative provider code.
   */
  async close(): Promise<void> {
    this.closed = true
    for (const controller of this.requests.keys()) controller.abort()
    await Promise.allSettled(this.requests.values())
  }

  async handle(incoming: IncomingMessage, outgoing: ServerResponse): Promise<Response> {
    if (!this.authenticated(incoming)) return rejection(401, 'unauthorized')
    if (this.closed || this.slots >= this.options.maxConcurrentRequests)
      return rejection(503, 'at_capacity')
    let work
    try {
      work = this.options.workBudget.reserve(operationWorkBytes)
    } catch {
      return rejection(503, 'at_capacity')
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
    let executionStarted = false
    let executionSettled = true
    let requestFinished = false
    const release = (): void => {
      work.release()
      this.slots -= 1
    }
    const acceptEnvelope = (raw: string): GatewayOperation => {
      operation = parseOperation(raw, this.capabilities, ceilingMs)
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
        return rejection(413, 'request_too_large')
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
      executionStarted = true
      executionSettled = false
      const accepted = operation
      // Install observation before invoking the callback (including sync throws).
      const execution = Promise.resolve()
        .then(() => {
          signal.throwIfAborted()
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
      if (operation && (executionStarted || signal.aborted)) {
        const unknown =
          executionStarted &&
          operation.kind !== 'read' &&
          (!(cause instanceof OperationRetryError) || cause.outcomeUnknown)
        return completion(operation, { outcome: unknown ? 'unknown' : 'failed' })
      }
      if (cause instanceof GatewayAtCapacityError || cause instanceof TemporaryStorageFullError) {
        return rejection(503, 'at_capacity')
      }
      return rejection(
        signal.aborted ? 408 : 400,
        signal.aborted ? 'deadline_exceeded' : 'invalid_operation',
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
  const raw =
    result.outcome === 'completed'
      ? serializeOperationResult({
          request_id: operation.requestId,
          outcome: result.outcome,
          payload: result.payload,
        })
      : serializeOperationResult({ request_id: operation.requestId, outcome: result.outcome })
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

function rejection(status: number, code: string): Response {
  return new Response(JSON.stringify({ error: code }), {
    status,
    headers: {
      'content-type': 'application/json',
      'cache-control': 'no-store',
      connection: 'close',
    },
  })
}
