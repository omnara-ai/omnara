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
const shutdownGraceMs = 10_000

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
  handle: (update: ToolCallUpdate) => Promise<boolean>
  check: () => Promise<void>
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
  const running = new Map<
    string,
    { done: Promise<boolean>; cancel: AbortController; completed: AbortController }
  >()
  const parked = new Map<string, { call: CustomToolCallRef; result: SubmitToolCallResultRequest }>()
  const failed = new Set<string>()
  const grace = new AbortController()
  let checkAt = 0
  let skipped = false
  let wake: (() => void) | undefined
  signal.addEventListener(
    'abort',
    () => {
      for (const { cancel } of running.values()) cancel.abort()
      AbortSignal.timeout(shutdownGraceMs).addEventListener(
        'abort',
        () => {
          grace.abort()
        },
        { once: true },
      )
    },
    { once: true },
  )

  async function withRetries<T>(
    request: (fetch: typeof globalThis.fetch) => Promise<T>,
    retrySignal: AbortSignal,
  ) {
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
        if (!(await abortableDelay(delayMs, retrySignal))) throw cause
      }
    }
  }

  async function submit(
    path: SubmitToolCallResultData['path'],
    body: SubmitToolCallResultRequest,
    call: CustomToolCallRef,
    completed: AbortSignal,
  ) {
    const deadline = AbortSignal.any([
      signal.aborted ? AbortSignal.timeout(shutdownGraceMs) : grace.signal,
      completed,
    ])
    const post = (result: SubmitToolCallResultRequest) =>
      withRetries(
        (fetch) => submitToolCallResult({ client, path, body: result, signal: deadline, fetch }),
        deadline,
      )
    try {
      try {
        await post(body)
      } catch (error) {
        if (!(error instanceof ApiError && (error.status === 400 || error.status === 413))) {
          throw error
        }
        onError(error, call)
        await post(failedResult(error))
      }
    } catch (error) {
      if (error instanceof ApiError && error.code === 'tool_call_already_completed') return
      throw error
    }
  }

  async function execute(
    call: CustomToolCallRef,
    handler: CustomToolHandler,
    callSignal: AbortSignal,
    completed: AbortSignal,
    stored: SubmitToolCallResultRequest | undefined,
  ) {
    const path = { ...options.path, agentID: call.agentID, toolCallID: call.toolCallID }
    let result = stored
    if (result === undefined) {
      const current = await withRetries(
        (fetch) => getToolCall({ client, path, signal: callSignal, fetch }),
        callSignal,
      ).catch((cause: unknown) => {
        if (cause instanceof ApiError && cause.status === 404) return undefined
        throw cause
      })
      if (current?.data.state !== 'ready') return
      if (options.agentProfileID !== undefined) {
        const { data } = await withRetries(
          (fetch) => getAgent({ client, path, signal: callSignal, fetch }),
          callSignal,
        )
        if (data.agent.agent_profile_id !== options.agentProfileID) return
      }
      callSignal.throwIfAborted()
      try {
        result = await handler({ ...call, input: current.data.input, signal: callSignal })
      } catch (cause) {
        callSignal.throwIfAborted()
        const error = asError(cause)
        onError(error, call)
        result = failedResult(error)
      }
    }
    try {
      await submit(path, result, call, completed)
    } catch (error) {
      if (!callSignal.aborted) parked.set(call.toolCallID, { call, result })
      throw error
    }
  }

  function start(call: CustomToolCallRef): boolean {
    const id = call.toolCallID
    const handler = handlers.get(call.name)
    if (handler === undefined || running.has(id) || failed.has(id)) return true
    if (signal.aborted) return false
    const stored = parked.get(id)
    if (stored === undefined && full()) {
      skipped = true
      return false
    }
    parked.delete(id)
    const cancel = new AbortController()
    const completed = new AbortController()
    const done = execute(call, handler, cancel.signal, completed.signal, stored?.result)
      .then(
        () => true,
        (cause: unknown) => {
          if (completed.signal.aborted) return true
          if (signal.aborted) return false
          failed.add(id)
          onError(asError(cause), call)
          return false
        },
      )
      .finally(() => {
        running.delete(id)
        if (skipped) wake?.()
      })
    running.set(id, { done, cancel, completed })
    return true
  }

  function full() {
    return running.size + parked.size >= concurrency
  }

  function settled() {
    return Promise.allSettled([...running.values()].map(({ done }) => done))
  }

  async function sweep() {
    if (Date.now() >= checkAt) {
      failed.clear()
      checkAt = Date.now() + sweepIntervalMs
      for (const { call } of parked.values()) start(call)
    }
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
      const { data: page } = await withRetries(
        (fetch) => listProjectToolCalls({ client, path: options.path, query, signal, fetch }),
        signal,
      )
      for (const call of page.data) {
        if (!start({ agentID: call.agent_id, toolCallID: call.id, name: call.name })) {
          return
        }
      }
      cursor = page.next_cursor ?? undefined
    } while (cursor !== undefined)
  }

  return {
    handle(update) {
      const id = update.tool_call_id
      if (update.state === 'completed') {
        const entry = running.get(id)
        entry?.cancel.abort()
        entry?.completed.abort()
        parked.delete(id)
      } else if (
        update.state === 'ready' &&
        update.agent_id !== undefined &&
        update.name !== undefined
      ) {
        failed.delete(id)
        const call = { agentID: update.agent_id, toolCallID: id, name: update.name }
        if (!start(call)) return Promise.resolve(false)
      }
      return running.get(id)?.done ?? Promise.resolve(true)
    },
    async check() {
      try {
        await sweep()
      } finally {
        await settled()
      }
    },
    async run() {
      try {
        while (!signal.aborted) {
          await sweep()
          if (skipped && !full()) continue
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
        await settled()
      }
    },
  }
}
