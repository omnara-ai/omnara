import type { OmnaraClient } from './client'
import { ApiError } from './errors'
import { getToolCall, listProjectToolCalls, submitToolCallResult } from './generated/sdk.gen'
import type {
  ListProjectToolCallsData,
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
  signal: AbortSignal
}

export type CustomToolCallRef = Omit<CustomToolCall, 'input' | 'signal'>
export type CustomToolHandler = (call: CustomToolCall) => Promise<SubmitToolCallResultRequest>

export type CustomToolOutcome =
  { status: 'completed' | 'ignored' | 'busy' | 'stopped' } | { status: 'failed'; error: Error }

export interface CustomToolCheckOptions {
  cursor?: string
  limit?: number
}

export interface CustomToolCheckResult {
  results: { call: CustomToolCallRef; outcome: CustomToolOutcome }[]
  nextCursor: string | null
}

export interface CreateCustomToolWorkerOptions {
  client: OmnaraClient
  path: ListProjectToolCallsData['path']
  agentProfileID?: string
  tools: Record<string, CustomToolHandler>
  concurrency?: number
  signal?: AbortSignal
  shutdownGraceMs?: number
  onError: (error: Error, call: CustomToolCallRef) => void | Promise<void>
}

export interface CustomToolWorker {
  /** Pass only authenticated SSE frames or signature-verified webhook updates. */
  handle: (update: ToolCallUpdate) => Promise<CustomToolOutcome>
  check: (options?: CustomToolCheckOptions) => Promise<CustomToolCheckResult>
  run: () => Promise<void>
  shutdown: () => Promise<void>
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
  const concurrency = options.concurrency ?? 10
  const shutdownGraceMs = options.shutdownGraceMs ?? 10_000
  if (!Number.isInteger(concurrency) || concurrency < 1) {
    throw new RangeError('concurrency must be a positive integer')
  }
  if (!Number.isInteger(shutdownGraceMs) || shutdownGraceMs < 0) {
    throw new RangeError('shutdownGraceMs must be a non-negative integer')
  }
  const stop = new AbortController()
  const deadline = new AbortController()
  const handlers = new Map(Object.entries(options.tools))
  interface Task {
    call: CustomToolCallRef
    cancel: AbortController
    completed: boolean
    submit: AbortController
    result?: SubmitToolCallResultRequest
  }
  const running = new Map<string, Task & { done: Promise<CustomToolOutcome> }>()
  const retainedResults = new Map<
    string,
    { call: CustomToolCallRef; result: SubmitToolCallResultRequest }
  >()
  let shutdownPromise: Promise<void> | undefined
  let runPromise: Promise<void> | undefined
  async function reportErrorSafely(error: Error, call: CustomToolCallRef): Promise<void> {
    try {
      await onError(error, call)
    } catch {
      return
    }
  }
  const capacityWaiters = new Set<() => void>()
  function capacityChanged() {
    for (const resolve of capacityWaiters) resolve()
    capacityWaiters.clear()
  }
  async function waitForSlot(
    call: CustomToolCallRef,
    waitForRetainedResults = false,
    until = Infinity,
  ) {
    while (
      !stop.signal.aborted &&
      Date.now() < until &&
      full() &&
      !running.has(call.toolCallID) &&
      !retainedResults.has(call.toolCallID) &&
      (waitForRetainedResults || running.size > 0)
    ) {
      await new Promise<void>((resolve) => {
        capacityWaiters.add(resolve)
      })
    }
  }

  // Custom authentication can stall before fetch receives the abort signal.
  function requestUntilAborted<T>(request: () => Promise<T>, signal: AbortSignal): Promise<T> {
    return new Promise((resolve, reject) => {
      const abort = () => {
        signal.removeEventListener('abort', abort)
        reject(asError(signal.reason))
      }
      if (signal.aborted) {
        abort()
        return
      }
      signal.addEventListener('abort', abort, { once: true })
      Promise.resolve()
        .then(() => {
          signal.throwIfAborted()
          return request()
        })
        .then(
          (value) => {
            signal.removeEventListener('abort', abort)
            resolve(value)
          },
          (cause: unknown) => {
            signal.removeEventListener('abort', abort)
            reject(asError(cause))
          },
        )
    })
  }

  async function withRetries<T>(
    request: (fetch: typeof globalThis.fetch) => Promise<T>,
    signal: AbortSignal,
  ): Promise<T> {
    for (let attempt = 1; ; attempt++) {
      signal.throwIfAborted()
      const dispatch = { started: false }
      try {
        return await requestUntilAborted(
          () =>
            request((input, init) => {
              signal.throwIfAborted()
              dispatch.started = true
              return (client.getConfig().fetch ?? globalThis.fetch)(input, init)
            }),
          signal,
        )
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

  async function execute(task: Task): Promise<CustomToolOutcome> {
    const { call, cancel } = task
    const path = { ...options.path, agentID: call.agentID, toolCallID: call.toolCallID }
    if (task.result === undefined) {
      const { data: current } = await withRetries(
        (fetch) => getToolCall({ client, path, signal: cancel.signal, fetch }),
        cancel.signal,
      )
      if (current.state !== 'ready')
        return { status: current.state === 'completed' ? 'completed' : 'ignored' }
      const handler = current.type === 'custom' ? handlers.get(current.name) : undefined
      if (handler === undefined) return { status: 'ignored' }
      cancel.signal.throwIfAborted()
      try {
        task.result = await handler({
          ...call,
          name: current.name,
          input: current.input,
          signal: cancel.signal,
        })
      } catch (cause) {
        cancel.signal.throwIfAborted()
        const error = asError(cause)
        void reportErrorSafely(error, call)
        task.result = failedResult(error)
      }
    }
    const signal = task.submit.signal
    const post = (body: SubmitToolCallResultRequest) =>
      withRetries((fetch) => submitToolCallResult({ client, path, body, signal, fetch }), signal)
    try {
      try {
        await post(task.result)
      } catch (cause) {
        if (!(cause instanceof ApiError && (cause.status === 400 || cause.status === 413)))
          throw cause
        void reportErrorSafely(cause, call)
        task.result = failedResult(cause)
        await post(task.result)
      }
    } catch (cause) {
      if (!(cause instanceof ApiError && cause.code === 'tool_call_already_completed')) throw cause
    }
    return { status: 'completed' }
  }

  function full() {
    return running.size + retainedResults.size >= concurrency
  }

  function start(call: CustomToolCallRef, draining = false): Promise<CustomToolOutcome> {
    const id = call.toolCallID
    const active = running.get(id)
    if (active !== undefined) return active.done
    if (stop.signal.aborted && !draining) return Promise.resolve({ status: 'stopped' })
    const stored = retainedResults.get(id)
    if (stored === undefined && full()) {
      return Promise.resolve({ status: 'busy' })
    }
    retainedResults.delete(id)
    const task: Task = {
      call,
      cancel: new AbortController(),
      completed: false,
      submit: new AbortController(),
      result: stored?.result,
    }
    let onDeadline: () => void
    const interrupted = new Promise<CustomToolOutcome>((_, reject) => {
      onDeadline = () => {
        task.submit.abort(deadline.signal.reason)
        reject(asError(deadline.signal.reason))
      }
      deadline.signal.addEventListener('abort', onDeadline, { once: true })
    })
    const done = Promise.race([
      // Register the task before entering customer code (including custom fetch/auth).
      Promise.resolve().then(() => execute(task)),
      interrupted,
    ])
      .catch((cause: unknown): CustomToolOutcome => {
        if (task.completed) return { status: 'completed' }
        if (stop.signal.aborted && task.result === undefined && !deadline.signal.aborted)
          return { status: 'stopped' }
        if (task.result !== undefined && !deadline.signal.aborted)
          retainedResults.set(id, { call, result: task.result })
        const error = asError(cause)
        void reportErrorSafely(error, call)
        return { status: 'failed', error }
      })
      .finally(() => {
        deadline.signal.removeEventListener('abort', onDeadline)
        running.delete(id)
        capacityChanged()
      })
    running.set(id, Object.assign(task, { done }))
    return done
  }

  function readPage(page: CustomToolCheckOptions) {
    return withRetries(
      (fetch) =>
        listProjectToolCalls({
          client,
          path: options.path,
          signal: stop.signal,
          fetch,
          query: {
            state: 'ready',
            type: 'custom',
            names: [...handlers.keys()],
            agent_profile_id: options.agentProfileID,
            cursor: page.cursor,
            limit: page.limit,
          },
        }),
      stop.signal,
    )
  }

  async function check(checkOptions: CustomToolCheckOptions = {}): Promise<CustomToolCheckResult> {
    stop.signal.throwIfAborted()
    const attempts = new Map<
      string,
      { call: CustomToolCallRef; done: Promise<CustomToolOutcome> }
    >()
    let nextCursor: string | null = null
    try {
      if (checkOptions.cursor === undefined) {
        for (const { call } of retainedResults.values())
          attempts.set(call.toolCallID, { call, done: start(call) })
      }
      if (handlers.size > 0) {
        const { data: page } = await readPage(checkOptions)
        nextCursor = page.next_cursor
        for (const current of page.data) {
          const call = { agentID: current.agent_id, toolCallID: current.id, name: current.name }
          if (attempts.has(call.toolCallID)) continue
          await waitForSlot(call)
          attempts.set(call.toolCallID, { call, done: start(call) })
        }
      }
    } finally {
      await Promise.all([...attempts.values()].map(({ done }) => done))
    }
    const results = await Promise.all(
      [...attempts.values()].map(async ({ call, done }) => ({ call, outcome: await done })),
    )
    return { results, nextCursor }
  }

  function shutdown(): Promise<void> {
    if (shutdownPromise !== undefined) return shutdownPromise
    // Assign before aborting: a customer's abort handler can call shutdown again.
    shutdownPromise = Promise.resolve().then(async () => {
      const timer = setTimeout(() => {
        deadline.abort(new Error('custom tool shutdown grace period expired'))
      }, shutdownGraceMs)
      try {
        // onError may initiate shutdown while the failed task is still finishing.
        const flushes = [...retainedResults.values()].map(async ({ call }) => {
          await running.get(call.toolCallID)?.done
          if (retainedResults.has(call.toolCallID) && !deadline.signal.aborted)
            await start(call, true)
        })
        await Promise.all([...flushes, ...[...running.values()].map(({ done }) => done)])
      } finally {
        clearTimeout(timer)
        deadline.abort(new Error('custom tool worker has shut down'))
        retainedResults.clear()
        options.signal?.removeEventListener('abort', onAbort)
      }
    })
    stop.abort()
    for (const task of running.values()) task.cancel.abort()
    capacityChanged()
    return shutdownPromise
  }

  function onAbort() {
    void shutdown()
  }
  if (options.signal?.aborted) void shutdown()
  else options.signal?.addEventListener('abort', onAbort, { once: true })

  return {
    handle(update) {
      if (
        update.org_id !== options.path.orgID ||
        update.project_id !== options.path.projectID ||
        (options.agentProfileID !== undefined && update.agent_profile_id !== options.agentProfileID)
      ) {
        return Promise.resolve({ status: 'ignored' })
      }
      const id = update.tool_call_id
      if (update.state === 'completed') {
        const task = running.get(id)
        if (task !== undefined) {
          task.completed = true
          task.submit.abort()
          task.cancel.abort()
        }
        retainedResults.delete(id)
        capacityChanged()
        return task?.done ?? Promise.resolve({ status: 'completed' })
      }
      if (update.state !== 'ready' || update.name === undefined || !handlers.has(update.name)) {
        return Promise.resolve({ status: 'ignored' })
      }
      return start({ agentID: update.agent_id, toolCallID: id, name: update.name })
    },
    check,
    run() {
      runPromise ??= Promise.resolve().then(async () => {
        const retryRetainedResults = () => {
          for (const { call } of retainedResults.values()) void start(call)
          capacityChanged()
        }
        const timer = setInterval(retryRetainedResults, sweepIntervalMs)
        try {
          retryRetainedResults()
          while (!stop.signal.aborted) {
            const until = Date.now() + sweepIntervalMs
            let cursor: string | undefined
            scan: do {
              if (handlers.size === 0) break
              const { data: page } = await readPage({ cursor })
              for (const current of page.data) {
                const call = {
                  agentID: current.agent_id,
                  toolCallID: current.id,
                  name: current.name,
                }
                if (retainedResults.has(call.toolCallID)) continue
                await waitForSlot(call, true, until)
                stop.signal.throwIfAborted()
                if (Date.now() >= until) break scan
                void start(call)
              }
              cursor = page.next_cursor ?? undefined
            } while (cursor !== undefined && Date.now() < until)
            // Restart periodically so older calls approved during this pass are seen.
            await abortableDelay(Math.max(0, until - Date.now()), stop.signal)
          }
        } catch (error) {
          if (!stop.signal.aborted) throw error
        } finally {
          clearInterval(timer)
          await shutdown()
        }
      })
      return runPromise
    },
    shutdown,
  }
}
