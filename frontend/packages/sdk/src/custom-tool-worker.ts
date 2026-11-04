import type { OmnaraClient } from './client'
import { ApiError } from './errors'
import {
  getAgent,
  getToolCall,
  listProjectToolCalls,
  submitToolCallResult,
} from './generated/sdk.gen'
import type {
  ListProjectToolCallsData,
  SubmitToolCallResultData,
  SubmitToolCallResultRequest,
  ToolCallUpdate,
  ToolInput,
} from './generated/types.gen'
import { abortableDelay, retryAfterMaximumDelayMs, retryDelay } from './retry'

const sweepIntervalMs = 60_000
const defaultConcurrency = 10

export interface CustomToolCall {
  agentID: string
  toolCallID: string
  name: string
  input: ToolInput
  signal: AbortSignal
}

type CustomToolCallRef = Omit<CustomToolCall, 'input' | 'signal'>

export type CustomToolHandler = (call: CustomToolCall) => Promise<SubmitToolCallResultRequest>

export interface CreateCustomToolWorkerOptions {
  client: OmnaraClient
  path: ListProjectToolCallsData['path']
  agentProfileID?: string
  tools: Record<string, CustomToolHandler>
  concurrency?: number
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
  const { client, onError } = options
  const concurrency = options.concurrency ?? defaultConcurrency
  if (!Number.isInteger(concurrency) || concurrency < 1) {
    throw new RangeError('concurrency must be a positive integer')
  }
  const stop = new AbortController()
  const signal =
    options.signal === undefined ? stop.signal : AbortSignal.any([options.signal, stop.signal])
  const handlers = new Map(Object.entries(options.tools))
  const running = new Map<string, Promise<void>>()
  const failed = new Set<string>()
  let skipped = false
  let wake: (() => void) | undefined

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
      const current = await withRetries((fetch) =>
        getToolCall({ client, path, signal, fetch }),
      ).catch((cause: unknown) => {
        if (cause instanceof ApiError && cause.status === 404) return undefined
        throw cause
      })
      if (current?.data.state !== 'ready') return
      if (options.agentProfileID !== undefined) {
        const { data } = await withRetries((fetch) => getAgent({ client, path, signal, fetch }))
        if (data.agent.agent_profile_id !== options.agentProfileID) return
      }
      input = current.data.input
    }
    signal.throwIfAborted()
    let result: SubmitToolCallResultRequest
    try {
      result = await handler({ ...call, input, signal })
    } catch (cause) {
      signal.throwIfAborted()
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

  function start(call: CustomToolCallRef, input: ToolInput | undefined): boolean {
    const handler = handlers.get(call.name)
    if (handler === undefined || running.has(call.toolCallID) || failed.has(call.toolCallID)) {
      return true
    }
    if (running.size >= concurrency) {
      skipped = true
      return false
    }
    running.set(
      call.toolCallID,
      execute(call, input, handler)
        .catch((cause: unknown) => {
          if (signal.aborted) return
          failed.add(call.toolCallID)
          onError(asError(cause), call)
        })
        .finally(() => {
          running.delete(call.toolCallID)
          if (skipped) wake?.()
        }),
    )
    return true
  }

  async function sweep() {
    skipped = false
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
        if (!start({ agentID: call.agent_id, toolCallID: call.id, name: call.name }, call.input)) {
          return
        }
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
        undefined,
      )
    },
    async run() {
      let checkAt = 0
      try {
        while (!signal.aborted) {
          if (Date.now() >= checkAt) {
            failed.clear()
            checkAt = Date.now() + sweepIntervalMs
          }
          await sweep()
          if (skipped && running.size < concurrency) continue
          const idle = new AbortController()
          wake = () => {
            idle.abort()
          }
          await abortableDelay(checkAt - Date.now(), AbortSignal.any([signal, idle.signal]))
        }
      } catch (error) {
        if (!signal.aborted) throw error
      } finally {
        stop.abort()
        await Promise.allSettled(running.values())
      }
    },
  }
}
