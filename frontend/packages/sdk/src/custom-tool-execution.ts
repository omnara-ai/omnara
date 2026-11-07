import type { OmnaraClient } from './client'
import { requestToolAPI, toolError } from './custom-tool-request'
import type {
  CustomToolCallRef,
  CustomToolHandler,
  CustomToolObservation,
  CustomToolOutcome,
  WorkSource,
} from './custom-tool-worker-types'
import { ApiError } from './errors'
import { getToolCall, submitToolCallResult } from './generated/sdk.gen'
import type {
  ListProjectToolCallsData,
  SubmitToolCallResultRequest,
  ToolInput,
} from './generated/types.gen'
import { zSubmitToolCallResultRequest } from './generated/zod.gen'
import { notifyObserver } from './observer'

interface ToolExecutionOptions {
  client: OmnaraClient
  path: ListProjectToolCallsData['path']
  requestTimeoutMs: number
  handler: CustomToolHandler | undefined
  onError: (error: Error, call: CustomToolCallRef) => Promise<void>
  onObserve?: (observation: CustomToolObservation) => void | Promise<void>
}

export interface PendingResult {
  body: SubmitToolCallResultRequest
  fallback: boolean
  retainedAt?: number
}
export interface CustomToolTask {
  call: CustomToolCallRef
  source: WorkSource
  // Only a just-read recovery page may supply input. Notifications always re-read.
  input?: ToolInput
  cancel: AbortController
  completed: boolean
  submit: AbortController
  result?: PendingResult
  rejected?: boolean
}

function failedResult(): SubmitToolCallResultRequest {
  return {
    outcome: 'failed',
    content_blocks: [{ type: 'text', text: 'The tool could not complete the request.' }],
  }
}

function submissionDisposition(error: Error) {
  if (error instanceof ApiError) {
    switch (error.code) {
      case 'tool_call_already_completed':
        return 'completed'
      case 'conflict':
      case 'state_transition_conflict':
        return 'not_ready'
      case 'invalid_request':
      case 'validation_failed':
      case 'request_too_large':
        return 'invalid'
    }
    // Proxies may reject an oversized body without the API's typed error envelope.
    if (error.status === 413) return 'invalid'
  }
  // Project authorization can hide existing calls behind a 404. Preserve the
  // result if access returns; completion is established by a typed response,
  // never inferred from visibility. Revisit this if individual calls gain purging.
  return 'retain'
}

export async function executeCustomTool(
  task: CustomToolTask,
  options: ToolExecutionOptions,
): Promise<CustomToolOutcome> {
  const { client, requestTimeoutMs, onError: reportErrorSafely } = options
  const observe = (event: CustomToolObservation) => {
    notifyObserver(options.onObserve, event)
  }
  const { call, cancel } = task
  const path = { ...options.path, agentID: call.agentID, toolCallID: call.toolCallID }
  if (task.result === undefined) {
    if (task.input === undefined) {
      const started = performance.now()
      let outcome: 'ready' | 'completed' | 'ignored' | 'failed' = 'failed'
      try {
        const { data: current } = await requestToolAPI(
          client,
          (fetch, signal, timeoutMs) => getToolCall({ client, path, signal, fetch, timeoutMs }),
          cancel.signal,
          requestTimeoutMs,
        )
        if (current.state !== 'ready') {
          outcome = current.state === 'completed' ? 'completed' : 'ignored'
          return { status: outcome }
        }
        if (current.type !== 'custom' || current.name !== call.name) {
          outcome = 'ignored'
          return { status: outcome }
        }
        task.input = current.input
        outcome = 'ready'
      } finally {
        observe({ stage: 'readiness', durationMs: performance.now() - started, outcome })
      }
    }
    const handler = options.handler
    if (handler === undefined) return { status: 'ignored' }
    cancel.signal.throwIfAborted()
    const started = performance.now()
    let outcome: 'succeeded' | 'failed' | 'deferred' | 'canceled' = 'canceled'
    try {
      const result = await handler({
        ...call,
        input: task.input,
        signal: cancel.signal,
      })
      if (task.completed) return { status: 'completed' }
      if (result.outcome === 'deferred') {
        outcome = 'deferred'
        return { status: 'deferred' }
      }
      const body = zSubmitToolCallResultRequest.parse(result)
      task.result = { body, fallback: false }
      outcome = body.outcome
    } catch (cause) {
      cancel.signal.throwIfAborted()
      const error = toolError(cause)
      void reportErrorSafely(error, call)
      task.result = { body: failedResult(), fallback: true }
      outcome = 'failed'
    } finally {
      observe({
        stage: 'handler',
        source: task.source,
        durationMs: performance.now() - started,
        outcome,
      })
    }
  }
  const signal = task.submit.signal
  const post = (body: SubmitToolCallResultRequest) =>
    requestToolAPI(
      client,
      (fetch, signal, timeoutMs) =>
        submitToolCallResult({ client, path, body, signal, fetch, timeoutMs }),
      signal,
      requestTimeoutMs,
    )
  while (true) {
    const started = performance.now()
    const retainedForMs =
      task.result.retainedAt === undefined ? 0 : Date.now() - task.result.retainedAt
    let outcome: 'accepted' | 'already_completed' | 'not_ready' | 'rejected' | 'failed' = 'failed'
    try {
      await post(task.result.body)
      outcome = 'accepted'
      return { status: 'completed' }
    } catch (cause) {
      const error = toolError(cause)
      switch (submissionDisposition(error)) {
        case 'completed':
          outcome = 'already_completed'
          return { status: 'completed' }
        case 'not_ready':
          outcome = 'not_ready'
          task.result = undefined
          throw cause
        case 'invalid':
          outcome = 'rejected'
          if (task.result.fallback) {
            task.rejected = true
            throw cause
          }
          void reportErrorSafely(error, call)
          task.result = {
            body: failedResult(),
            fallback: true,
            retainedAt: task.result.retainedAt,
          }
          break
        default:
          throw cause
      }
    } finally {
      observe({
        stage: 'submission',
        durationMs: performance.now() - started,
        retainedForMs,
        outcome,
      })
    }
  }
}
