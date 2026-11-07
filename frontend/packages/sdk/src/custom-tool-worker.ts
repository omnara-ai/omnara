import { forwardAbort, untilAborted } from './abort'
import {
  type CustomToolTask as Task,
  executeCustomTool,
  type PendingResult,
} from './custom-tool-execution'
import { requestToolAPI, toolError, ToolRequestError } from './custom-tool-request'
import type {
  CreateCustomToolWorkerOptions,
  CustomToolCallRef,
  CustomToolCheckOptions,
  CustomToolCheckResult,
  CustomToolObservation,
  CustomToolOutcome,
  CustomToolWorker,
} from './custom-tool-worker-types'
import { listProjectToolCalls } from './generated/sdk.gen'
import type { ToolInput } from './generated/types.gen'
import { notifyObserver } from './observer'
import { abortableDelay } from './retry'

const sweepIntervalMs = 60_000
const settledCallLimit = 1_000

type Admission =
  | { source: 'notification'; signal?: AbortSignal }
  | { source: 'recovery'; input: ToolInput; signal?: AbortSignal }
  | { source: 'retained'; signal?: AbortSignal; draining?: boolean }

export function createCustomToolWorker(options: CreateCustomToolWorkerOptions): CustomToolWorker {
  const { client, onError } = options
  const concurrency = options.concurrency ?? 10
  const shutdownGraceMs = options.shutdownGraceMs ?? 10_000
  const requestTimeoutMs = options.requestTimeoutMs ?? 30_000
  if (!Number.isInteger(concurrency) || concurrency < 1) {
    throw new RangeError('concurrency must be a positive integer')
  }
  if (
    !Number.isInteger(shutdownGraceMs) ||
    shutdownGraceMs < 0 ||
    shutdownGraceMs > 2_147_483_647
  ) {
    throw new RangeError('shutdownGraceMs must be an integer between 0 and 2147483647')
  }
  if (
    !Number.isInteger(requestTimeoutMs) ||
    requestTimeoutMs < 1 ||
    requestTimeoutMs > 2_147_483_647
  ) {
    throw new RangeError('requestTimeoutMs must be an integer between 1 and 2147483647')
  }
  const stop = new AbortController()
  const deadline = new AbortController()
  const handlers = new Map(Object.entries(options.tools))
  const running = new Map<string, Task & { done: Promise<CustomToolOutcome> }>()
  const retainedResults = new Map<
    string,
    { call: CustomToolCallRef; result: PendingResult; retryAt: number }
  >()
  const settledCalls = new Map<string, CustomToolOutcome>()
  function rememberSettled(id: string, outcome: CustomToolOutcome) {
    settledCalls.delete(id)
    settledCalls.set(id, outcome)
    const oldest = settledCalls.keys().next().value
    if (settledCalls.size > settledCallLimit && oldest !== undefined) settledCalls.delete(oldest)
  }
  let shutdownPromise: Promise<void> | undefined
  let runPromise: Promise<void> | undefined
  let reserved = 0
  function observe(observation: CustomToolObservation) {
    notifyObserver(options.onObserve, observation)
  }
  async function reportErrorSafely(error: Error, call?: CustomToolCallRef): Promise<void> {
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
    observe({ stage: 'capacity', running: running.size, retained: retainedResults.size, reserved })
  }
  async function reserveSlots(limit: number, signal: AbortSignal, waitForRetained: boolean) {
    while (full()) {
      signal.throwIfAborted()
      // A standalone check has no timer that could unblock retained submissions.
      if (!waitForRetained && running.size === 0 && reserved === 0) return 0
      await new Promise<void>((resolve) => {
        function finish() {
          signal.removeEventListener('abort', finish)
          capacityWaiters.delete(finish)
          resolve()
        }
        capacityWaiters.add(finish)
        signal.addEventListener('abort', finish, { once: true })
      })
    }
    signal.throwIfAborted()
    const count = Math.min(limit, concurrency - running.size - retainedResults.size - reserved)
    reserved += count
    capacityChanged()
    return count
  }

  function full() {
    return running.size + retainedResults.size + reserved >= concurrency
  }

  function start(
    call: CustomToolCallRef,
    request: Admission = { source: 'retained' },
  ): Promise<CustomToolOutcome> {
    const { source, signal: operationSignal } = request
    const draining = source === 'retained' && request.draining
    const id = call.toolCallID
    const admission = (outcome: 'started' | 'joined' | 'settled' | 'busy' | 'stopped') => {
      observe({ stage: 'admission', source, outcome })
    }
    if (operationSignal?.aborted) {
      admission('stopped')
      return Promise.resolve({ status: 'stopped' })
    }
    const active = running.get(id)
    if (active !== undefined) {
      admission('joined')
      return waitForTask(active.done, operationSignal)
    }
    if (stop.signal.aborted && !draining) {
      admission('stopped')
      return Promise.resolve({ status: 'stopped' })
    }
    const settled = settledCalls.get(id)
    if (settled !== undefined) {
      admission('settled')
      return Promise.resolve(settled)
    }
    const stored = retainedResults.get(id)
    if (stored !== undefined && stored.retryAt > Date.now()) {
      admission('busy')
      return Promise.resolve({ status: 'busy' })
    }
    if (stored === undefined && full()) {
      admission('busy')
      return Promise.resolve({ status: 'busy' })
    }
    retainedResults.delete(id)
    const task: Task = {
      call,
      source,
      input: request.source === 'recovery' ? request.input : undefined,
      cancel: new AbortController(),
      completed: false,
      submit: new AbortController(),
      result: stored?.result,
    }
    const unlinkCancel = forwardAbort(operationSignal, task.cancel)
    const unlinkSubmit = forwardAbort(operationSignal, task.submit)
    let onDeadline: () => void
    const interrupted = new Promise<CustomToolOutcome>((_, reject) => {
      onDeadline = () => {
        task.submit.abort(deadline.signal.reason)
        reject(toolError(deadline.signal.reason))
      }
      deadline.signal.addEventListener('abort', onDeadline, { once: true })
    })
    const done = Promise.race([
      // Register the task before entering customer code (including custom fetch/auth).
      Promise.resolve().then(() =>
        executeCustomTool(task, {
          client,
          path: options.path,
          requestTimeoutMs,
          handler: handlers.get(call.name),
          onError: reportErrorSafely,
          onObserve: options.onObserve,
        }),
      ),
      interrupted,
    ])
      .then((outcome) => {
        if (outcome.status === 'completed') rememberSettled(id, outcome)
        return outcome
      })
      .catch((cause: unknown): CustomToolOutcome => {
        if (task.completed) return { status: 'completed' }
        if (stop.signal.aborted && task.result === undefined && !deadline.signal.aborted)
          return { status: 'stopped' }
        const error = toolError(cause)
        if (task.result !== undefined && !deadline.signal.aborted && !task.rejected) {
          task.result.retainedAt ??= Date.now()
          retainedResults.set(id, {
            call,
            result: task.result,
            retryAt: cause instanceof ToolRequestError ? cause.retryAt : 0,
          })
        }
        if (task.rejected) rememberSettled(id, { status: 'failed', error })
        if (operationSignal?.aborted && !deadline.signal.aborted) return { status: 'stopped' }
        void reportErrorSafely(error, call)
        return { status: 'failed', error }
      })
      .finally(() => {
        deadline.signal.removeEventListener('abort', onDeadline)
        unlinkCancel()
        unlinkSubmit()
        running.delete(id)
        capacityChanged()
      })
    running.set(id, Object.assign(task, { done }))
    admission('started')
    capacityChanged()
    return waitForTask(done, operationSignal)
  }

  function waitForTask(
    done: Promise<CustomToolOutcome>,
    signal?: AbortSignal,
  ): Promise<CustomToolOutcome> {
    return signal === undefined
      ? done
      : untilAborted(() => done, signal).catch((): CustomToolOutcome => ({ status: 'stopped' }))
  }

  function readPage(page: CustomToolCheckOptions, signal: AbortSignal) {
    return requestToolAPI(
      client,
      (fetch, signal, timeoutMs) =>
        listProjectToolCalls({
          client,
          path: options.path,
          signal,
          fetch,
          timeoutMs,
          query: {
            state: 'ready',
            type: 'custom',
            names: handlers.size <= 100 ? [...handlers.keys()] : undefined,
            agent_profile_id: options.agentProfileID,
            cursor: page.cursor,
            limit: page.limit,
          },
        }),
      signal,
      requestTimeoutMs,
    )
  }

  interface Attempt {
    call: CustomToolCallRef
    done: Promise<CustomToolOutcome>
  }

  async function readAndStart({
    cursor,
    limit,
    readSignal,
    handlerSignal,
    waitForRetained,
    onAttempt,
  }: {
    cursor?: string
    limit: number
    readSignal: AbortSignal
    handlerSignal?: AbortSignal
    waitForRetained: boolean
    onAttempt?: (attempt: Attempt) => void
  }) {
    const started = performance.now()
    let remaining = await reserveSlots(limit, readSignal, waitForRetained)
    const waitMs = performance.now() - started
    if (remaining === 0) {
      observe({
        stage: 'recovery',
        durationMs: waitMs,
        waitMs,
        outcome: 'blocked',
        rows: 0,
        started: 0,
      })
      return undefined
    }
    try {
      const { data: page } = await readPage({ cursor, limit: remaining }, readSignal)
      readSignal.throwIfAborted()
      if (page.data.length > remaining)
        throw new Error('tool-call page exceeded its requested limit')
      let admitted = 0
      for (const current of page.data) {
        // Transfer the reservation synchronously; no capacity wait after the read.
        remaining--
        reserved--
        if (!handlers.has(current.name) || current.type !== 'custom' || current.state !== 'ready')
          continue
        const call = { agentID: current.agent_id, toolCallID: current.id, name: current.name }
        if (retainedResults.has(call.toolCallID)) continue
        if (!running.has(call.toolCallID) && !settledCalls.has(call.toolCallID)) admitted++
        const done = start(call, {
          source: 'recovery',
          input: current.input,
          signal: handlerSignal,
        })
        onAttempt?.({ call, done })
      }
      observe({
        stage: 'recovery',
        durationMs: performance.now() - started,
        waitMs,
        outcome: 'page',
        rows: page.data.length,
        started: admitted,
      })
      return page
    } catch (cause) {
      observe({
        stage: 'recovery',
        durationMs: performance.now() - started,
        waitMs,
        outcome: 'failed',
        rows: 0,
        started: 0,
      })
      throw cause
    } finally {
      reserved -= remaining
      capacityChanged()
    }
  }

  async function check(checkOptions: CustomToolCheckOptions = {}): Promise<CustomToolCheckResult> {
    const limit = checkOptions.limit ?? 50
    if (!Number.isInteger(limit) || limit < 1 || limit > 100) {
      throw new RangeError('limit must be an integer between 1 and 100')
    }
    stop.signal.throwIfAborted()
    const operation = new AbortController()
    const unlinkStop = forwardAbort(stop.signal, operation)
    const unlinkCaller = forwardAbort(checkOptions.signal, operation)
    const signal = operation.signal
    const attempts = new Map<string, Attempt>()
    let nextCursor = handlers.size === 0 ? null : (checkOptions.cursor ?? null)
    let blocked = false
    try {
      signal.throwIfAborted()
      for (const { call } of retainedResults.values())
        attempts.set(call.toolCallID, {
          call,
          done: start(call, { source: 'retained', signal: checkOptions.signal }),
        })
      let remaining = limit
      while (handlers.size > 0 && remaining > 0) {
        const page = await readAndStart({
          cursor: nextCursor ?? undefined,
          limit: remaining,
          readSignal: signal,
          handlerSignal: checkOptions.signal,
          waitForRetained: false,
          onAttempt: (attempt) => attempts.set(attempt.call.toolCallID, attempt),
        })
        if (page === undefined) {
          blocked = true
          break
        }
        nextCursor = page.next_cursor
        remaining -= page.data.length
        if (nextCursor === null) break
      }
    } catch (cause) {
      throw toolError(cause)
    } finally {
      try {
        const settled = () => Promise.all([...attempts.values()].map(({ done }) => done))
        if (checkOptions.signal) await untilAborted(settled, checkOptions.signal)
        else await settled()
      } finally {
        unlinkStop()
        unlinkCaller()
      }
    }
    const results = await Promise.all(
      [...attempts.values()].map(async ({ call, done }) => ({ call, outcome: await done })),
    )
    return { results, nextCursor, blocked }
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
          const stored = retainedResults.get(call.toolCallID)
          if (stored === undefined) return
          if (stored.retryAt > Date.now())
            await abortableDelay(stored.retryAt - Date.now(), deadline.signal)
          if (!retainedResults.has(call.toolCallID)) return
          if (deadline.signal.aborted) {
            void reportErrorSafely(toolError(deadline.signal.reason), call)
            return
          }
          await start(call, { source: 'retained', draining: true })
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
    handle(update, operation = {}) {
      if (
        update.org_id !== options.path.orgID ||
        update.project_id !== options.path.projectID ||
        (options.agentProfileID !== undefined && update.agent_profile_id !== options.agentProfileID)
      ) {
        return Promise.resolve({ status: 'ignored' })
      }
      const id = update.tool_call_id
      if (update.state === 'completed') {
        rememberSettled(id, { status: 'completed' })
        const task = running.get(id)
        if (task !== undefined) {
          task.completed = true
          task.submit.abort()
          task.cancel.abort()
        }
        retainedResults.delete(id)
        capacityChanged()
        return task === undefined
          ? Promise.resolve({ status: 'completed' })
          : waitForTask(task.done, operation.signal)
      }
      if (update.state !== 'ready' || update.name === undefined || !handlers.has(update.name)) {
        return Promise.resolve({ status: 'ignored' })
      }
      return start(
        { agentID: update.agent_id, toolCallID: id, name: update.name },
        { source: 'notification', signal: operation.signal },
      )
    },
    check,
    run() {
      runPromise ??= Promise.resolve().then(async () => {
        const retryRetainedResults = () => {
          for (const { call } of retainedResults.values()) void start(call)
        }
        const timer = setInterval(retryRetainedResults, sweepIntervalMs)
        try {
          retryRetainedResults()
          let cursor: string | undefined
          let passStarted = 0
          while (!stop.signal.aborted) {
            if (cursor === undefined) passStarted = performance.now()
            const until = Date.now() + sweepIntervalMs
            do {
              if (handlers.size === 0) break
              try {
                const page = await readAndStart({
                  cursor,
                  limit: 100,
                  readSignal: stop.signal,
                  waitForRetained: true,
                })
                // The continuous loop waits for retained submissions to free capacity.
                if (page === undefined) break
                cursor = page.next_cursor ?? undefined
                if (cursor === undefined) {
                  observe({
                    stage: 'recovery',
                    durationMs: performance.now() - passStarted,
                    outcome: 'complete',
                  })
                }
              } catch (cause) {
                stop.signal.throwIfAborted()
                void reportErrorSafely(toolError(cause))
                const retryAt = cause instanceof ToolRequestError ? cause.retryAt : 0
                await abortableDelay(
                  Math.max(0, Math.max(until, retryAt) - Date.now()),
                  stop.signal,
                )
                break
              }
            } while (cursor !== undefined && Date.now() < until)
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
