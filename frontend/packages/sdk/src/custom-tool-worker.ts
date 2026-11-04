import type { OmnaraClient } from './client'
import { ApiError } from './errors'
import { getToolCall, listProjectToolCalls, submitToolCallResult } from './generated/sdk.gen'
import type {
  ListProjectToolCallsData,
  SubmitToolCallResultData,
  SubmitToolCallResultRequest,
  ToolCallUpdate,
  ToolInput,
} from './generated/types.gen'
import { abortableDelay, retryAfterMaximumDelayMs, retryDelay } from './retry'

const sweepIntervalMs = 60_000

export interface CustomToolCall {
  agentID: string
  toolCallID: string
  name: string
  input: ToolInput
}

type CustomToolCallRef = Omit<CustomToolCall, 'input'>

export type CustomToolHandler = (call: CustomToolCall) => Promise<SubmitToolCallResultRequest>

export interface CreateCustomToolWorkerOptions {
  client: OmnaraClient
  path: ListProjectToolCallsData['path']
  agentProfileID?: string
  tools: Record<string, CustomToolHandler>
  signal?: AbortSignal
  onError: (error: Error, call: CustomToolCallRef) => void
}

export interface CustomToolWorker {
  handle: (update: ToolCallUpdate) => void
  run: () => Promise<void>
}

function asError(cause: unknown): Error {
  return cause instanceof Error ? cause : new Error(String(cause))
}

function retryable(cause: unknown, dispatched: boolean): boolean {
  if (cause instanceof ApiError) {
    return cause.status === 408 || cause.status === 429 || cause.status >= 500
  }
  return dispatched && cause instanceof TypeError
}

function failedResult(error: Error): SubmitToolCallResultRequest {
  return {
    outcome: 'failed',
    content_blocks: [{ type: 'structured_data', value: { error: error.message } }],
  }
}

export function createCustomToolWorker(options: CreateCustomToolWorkerOptions): CustomToolWorker {
  const { client, signal, onError } = options
  const handlers = new Map(Object.entries(options.tools))
  const running = new Set<string>()

  async function withRetries<T>(request: (fetch: typeof globalThis.fetch) => Promise<T>) {
    for (let attempt = 1; ; attempt++) {
      const dispatch = { started: false }
      try {
        return await request((input, init) => {
          dispatch.started = true
          return (client.getConfig().fetch ?? globalThis.fetch)(input, init)
        })
      } catch (cause) {
        if (!retryable(cause, dispatch.started)) throw cause
        const retryAfterMs = cause instanceof ApiError ? (cause.retryAfterMs ?? 0) : 0
        const delayMs = Math.max(
          retryDelay(attempt),
          Math.min(retryAfterMs, retryAfterMaximumDelayMs),
        )
        if (!(await abortableDelay(delayMs, signal))) throw cause
      }
    }
  }

  async function submit(path: SubmitToolCallResultData['path'], body: SubmitToolCallResultRequest) {
    try {
      await withRetries((fetch) => submitToolCallResult({ client, path, body, signal, fetch }))
    } catch (error) {
      if (!(error instanceof ApiError && error.code === 'tool_call_already_completed')) throw error
    }
  }

  async function execute(
    call: CustomToolCallRef,
    input: ToolInput | undefined,
    handler: CustomToolHandler,
  ) {
    const path = { ...options.path, agentID: call.agentID, toolCallID: call.toolCallID }
    if (input === undefined) {
      const { data } = await withRetries((fetch) => getToolCall({ client, path, signal, fetch }))
      if (data.state !== 'ready') return
      input = data.input
    }
    signal?.throwIfAborted()
    let result: SubmitToolCallResultRequest
    try {
      result = await handler({ ...call, input })
    } catch (cause) {
      const error = asError(cause)
      onError(error, call)
      result = failedResult(error)
    }
    try {
      await submit(path, result)
    } catch (error) {
      if (!(error instanceof ApiError && error.status === 400)) throw error
      onError(error, call)
      await submit(path, failedResult(error))
    }
  }

  function start(call: CustomToolCallRef, input: ToolInput | undefined) {
    const handler = handlers.get(call.name)
    if (handler === undefined || running.has(call.toolCallID)) return
    running.add(call.toolCallID)
    void execute(call, input, handler)
      .catch((cause: unknown) => {
        if (signal?.aborted !== true) onError(asError(cause), call)
      })
      .finally(() => running.delete(call.toolCallID))
  }

  async function sweep() {
    let cursor: string | undefined
    do {
      const query: ListProjectToolCallsData['query'] = {
        state: 'ready',
        type: 'custom',
        names: [...handlers.keys()],
        agent_profile_id: options.agentProfileID,
        cursor,
      }
      const { data: page } = await withRetries((fetch) =>
        listProjectToolCalls({ client, path: options.path, query, signal, fetch }),
      )
      for (const call of page.data) {
        start({ agentID: call.agent_id, toolCallID: call.id, name: call.name }, call.input)
      }
      cursor = page.next_cursor ?? undefined
    } while (cursor !== undefined)
  }

  return {
    handle(update) {
      if (update.state !== 'ready' || update.agent_id === undefined || update.name === undefined) {
        return
      }
      start(
        { agentID: update.agent_id, toolCallID: update.tool_call_id, name: update.name },
        update.input,
      )
    },
    async run() {
      try {
        do {
          await sweep()
        } while (await abortableDelay(sweepIntervalMs, signal))
      } catch (error) {
        if (signal?.aborted !== true) throw error
      }
    },
  }
}
