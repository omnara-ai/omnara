import { forwardAbort, untilAborted } from './abort'
import {
  type CustomToolTask,
  type CustomToolWork,
  executeCustomTool,
} from './custom-tool-execution'
import { toolError } from './custom-tool-request'
import type {
  CreateCustomToolWorkerOptions,
  CustomToolCallRef,
  CustomToolCheckOptions,
  CustomToolCheckResult,
  CustomToolOutcome,
  CustomToolWorker,
} from './custom-tool-worker-types'
import { listProjectToolCalls } from './generated/sdk.gen'
import { zListProjectToolCallsQuery } from './generated/zod.gen'
import { notifyObserver } from './observer'

export function createCustomToolWorker(options: CreateCustomToolWorkerOptions): CustomToolWorker {
  const handlers = new Map(Object.entries(options.tools))
  const nameSchema = zListProjectToolCallsQuery.shape.names.unwrap().element
  for (const name of handlers.keys()) {
    const result = nameSchema.safeParse(name)
    if (!result.success)
      throw new TypeError(
        `Invalid custom tool name ${JSON.stringify(name)}: ${result.error.message}`,
      )
  }
  const running = new Map<string, CustomToolTask & { done: Promise<CustomToolOutcome> }>()
  // Only outstanding list reads need protection from a completion newer than their snapshot.
  const listingCompletions = new Set<Set<string>>()
  function observedCompletion(id: string) {
    for (const completed of listingCompletions) completed.add(id)
  }
  const observe = options.onObserve
  const report = (cause: unknown, call?: CustomToolCallRef) => {
    notifyObserver((error: Error) => options.onError?.(error, call), toolError(cause))
  }

  function start(work: CustomToolWork, signal?: AbortSignal): Promise<CustomToolOutcome> {
    signal?.throwIfAborted()
    const { call, source } = work
    const handler = handlers.get(call.name)
    if (handler === undefined) return Promise.resolve({ status: 'ignored' })
    const id = `${call.agentID}/${call.toolCallID}`
    const active = running.get(id)
    if (active !== undefined) {
      notifyObserver(observe, { stage: 'admission', source, outcome: 'joined' })
      return signal ? untilAborted(() => active.done, signal) : active.done
    }
    const task: CustomToolTask = { ...work, cancel: new AbortController(), completed: false }
    const unlink = forwardAbort(signal, task.cancel)
    const done = Promise.resolve()
      .then(() => executeCustomTool(task, { ...options, handler }))
      .catch((cause: unknown): CustomToolOutcome => {
        if (task.completed) return { status: 'completed' }
        report(cause, task.call)
        throw cause
      })
      .then((outcome) => {
        if (outcome.status === 'completed') observedCompletion(id)
        return outcome
      })
      .finally(() => {
        unlink()
        running.delete(id)
      })
    // Register before executing customer code, including observers and authentication.
    running.set(id, Object.assign(task, { done }))
    notifyObserver(observe, { stage: 'admission', source, outcome: 'started' })
    return done
  }

  async function check(checkOptions: CustomToolCheckOptions = {}): Promise<CustomToolCheckResult> {
    const query = zListProjectToolCallsQuery.parse({
      state: 'ready',
      type: 'custom',
      names: handlers.size > 0 && handlers.size <= 100 ? [...handlers.keys()] : undefined,
      agent_profile_id: options.agentProfileID,
      cursor: checkOptions.cursor,
      limit: checkOptions.limit,
    })
    checkOptions.signal?.throwIfAborted()
    if (handlers.size === 0) return { results: [], nextCursor: null }
    const started = performance.now()
    let rows = 0
    let outcome: 'page' | 'failed' = 'failed'
    const completed = new Set<string>()
    listingCompletions.add(completed)
    try {
      const { data: page } = await listProjectToolCalls({
        client: options.client,
        path: options.path,
        query,
        signal: checkOptions.signal,
        throwOnError: true,
        responseStyle: 'fields',
      })
      if (page.data.length > query.limit)
        throw new Error('tool-call page exceeded its requested limit')
      rows = page.data.length
      const pending = page.data
        .filter(
          (call) => call.type === 'custom' && call.state === 'ready' && handlers.has(call.name),
        )
        .map(async (current) => {
          const call = { agentID: current.agent_id, toolCallID: current.id, name: current.name }
          try {
            return {
              call,
              outcome: completed.has(`${call.agentID}/${call.toolCallID}`)
                ? { status: 'completed' as const }
                : await start({ call, source: 'recovery', current }, checkOptions.signal),
            }
          } catch (cause) {
            return { call, outcome: { status: 'failed' as const, error: toolError(cause) } }
          }
        })
      listingCompletions.delete(completed)
      const results = await Promise.all(pending)
      outcome = 'page'
      return { results, nextCursor: page.next_cursor }
    } catch (cause) {
      report(cause)
      throw cause
    } finally {
      listingCompletions.delete(completed)
      notifyObserver(observe, {
        stage: 'recovery',
        durationMs: performance.now() - started,
        outcome,
        rows,
      })
    }
  }

  return {
    async handle(update, operation = {}) {
      if (
        update.org_id !== options.path.orgID ||
        update.project_id !== options.path.projectID ||
        (options.agentProfileID !== undefined && update.agent_profile_id !== options.agentProfileID)
      )
        return { status: 'ignored' }
      if (update.state === 'completed') {
        const id = `${update.agent_id}/${update.tool_call_id}`
        observedCompletion(id)
        const task = running.get(id)
        if (task) {
          task.completed = true
          task.cancel.abort()
          if (operation.signal) await untilAborted(() => task.done, operation.signal)
          else await task.done
        }
        return { status: 'completed' }
      }
      if (update.state !== 'ready' || update.name === undefined || !handlers.has(update.name))
        return { status: 'ignored' }
      return start(
        {
          call: { agentID: update.agent_id, toolCallID: update.tool_call_id, name: update.name },
          source: 'notification',
        },
        operation.signal,
      )
    },
    check,
  }
}
