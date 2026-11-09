import type { OmnaraClient } from './client'
import { CustomToolDeliveryError, deliverToolResult, toolError } from './custom-tool-request'
import type {
  CustomToolCallRef,
  CustomToolHandler,
  CustomToolObservation,
  CustomToolOutcome,
} from './custom-tool-worker-types'
import { ApiError } from './errors'
import { jsonBodySerializer } from './generated/core/bodySerializer.gen'
import { getToolCall, submitToolCallResult } from './generated/sdk.gen'
import type {
  ListProjectToolCallsData,
  SubmitToolCallResultRequest,
  ToolCall,
} from './generated/types.gen'
import { zSubmitToolCallResultRequest } from './generated/zod.gen'
import { notifyObserver } from './observer'

interface ToolExecutionOptions {
  client: OmnaraClient
  path: ListProjectToolCallsData['path']
  handler: CustomToolHandler
  onError?: (error: Error, call: CustomToolCallRef) => void | Promise<void>
  onObserve?: (observation: CustomToolObservation) => void | Promise<void>
}

export type CustomToolWork = {
  call: CustomToolCallRef
} & ({ source: 'notification' } | { source: 'recovery'; current: ToolCall })

export type CustomToolTask = CustomToolWork & {
  cancel: AbortController
  completed: boolean
}

function failedResult(): SubmitToolCallResultRequest {
  return {
    outcome: 'failed',
    content_blocks: [{ type: 'text', text: 'The tool could not complete the request.' }],
  }
}

export async function executeCustomTool(
  task: CustomToolTask,
  options: ToolExecutionOptions,
): Promise<CustomToolOutcome> {
  const { client } = options
  const observe = (event: CustomToolObservation) => {
    notifyObserver(options.onObserve, event)
  }
  const { call, cancel } = task
  const { signal } = cancel
  const path = { ...options.path, agentID: call.agentID, toolCallID: call.toolCallID }
  let current: ToolCall
  if (task.source === 'recovery') {
    current = task.current
  } else {
    // Notifications can be delayed or omit input; discovery already read the full call.
    const readStarted = performance.now()
    let readiness: 'ready' | 'completed' | 'ignored' | 'failed' = 'failed'
    try {
      current = (
        await getToolCall({
          client,
          path,
          signal,
          throwOnError: true,
          responseStyle: 'fields',
        })
      ).data
      if (current.state !== 'ready') {
        readiness = current.state === 'completed' ? 'completed' : 'ignored'
        return { status: readiness }
      }
      if (current.type !== 'custom' || current.name !== call.name) {
        readiness = 'ignored'
        return { status: readiness }
      }
      readiness = 'ready'
    } finally {
      observe({
        stage: 'readiness',
        durationMs: performance.now() - readStarted,
        outcome: readiness,
      })
    }
  }
  signal.throwIfAborted()
  const handlerStarted = performance.now()
  let handlerOutcome: 'succeeded' | 'failed' | 'deferred' | 'canceled' = 'canceled'
  let body: SubmitToolCallResultRequest
  try {
    // Await cooperative cancellation so a handler's late computed result is never lost.
    const result = await options.handler({ ...call, input: current.input, signal })
    if (task.completed) return { status: 'completed' }
    if (result.outcome === 'deferred') {
      signal.throwIfAborted()
      handlerOutcome = 'deferred'
      return { status: 'deferred' }
    }
    // Use the API serializer (including BigInt handling), then validate its JSON value.
    // The snapshot owns nested structured data; application mutations cannot change delivery.
    body = zSubmitToolCallResultRequest.parse(
      JSON.parse(jsonBodySerializer.bodySerializer(zSubmitToolCallResultRequest.parse(result))),
    )
    handlerOutcome = body.outcome
  } catch (cause) {
    if (task.completed) return { status: 'completed' }
    signal.throwIfAborted()
    notifyObserver((error: Error) => options.onError?.(error, call), toolError(cause))
    body = failedResult()
    handlerOutcome = 'failed'
  } finally {
    observe({
      stage: 'handler',
      source: task.source,
      durationMs: performance.now() - handlerStarted,
      outcome: handlerOutcome,
    })
  }

  const submitStarted = performance.now()
  let submission: 'accepted' | 'already_completed' | 'failed' = 'failed'
  try {
    await deliverToolResult(client, call, body, (fetch) =>
      submitToolCallResult({
        client,
        path,
        body,
        signal,
        fetch,
        throwOnError: true,
        responseStyle: 'fields',
      }),
    )
    submission = 'accepted'
    return { status: 'completed' }
  } catch (error) {
    if (
      // A completed update can change this flag while the POST is awaiting a response.
      // oxlint-disable-next-line typescript/no-unnecessary-condition
      task.completed ||
      (error instanceof CustomToolDeliveryError &&
        error.cause instanceof ApiError &&
        error.cause.status === 409 &&
        error.cause.code === 'tool_call_already_completed')
    ) {
      submission = 'already_completed'
      return { status: 'completed' }
    }
    throw error
  } finally {
    observe({
      stage: 'submission',
      durationMs: performance.now() - submitStarted,
      outcome: submission,
    })
  }
}
