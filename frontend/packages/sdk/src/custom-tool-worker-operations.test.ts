import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { CustomToolDeliveryError, toolError } from './custom-tool-request'
import { createCustomToolWorker } from './custom-tool-worker'
import {
  accepted,
  api,
  failed,
  found,
  path,
  ready,
  toolCall,
} from './custom-tool-worker-test-support'
import { ApiError } from './errors'
import { submitToolCallResult } from './generated/sdk.gen'
import type { SubmitToolCallResultRequest } from './generated/types.gen'

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
  vi.spyOn(Math, 'random').mockReturnValue(0.5)
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

it('runs once, exhausts shared HTTP retries, hands off unchanged output, and lets the caller resubmit', async () => {
  const value = { n: 1, large: 12345678901234567890n }
  const result: SubmitToolCallResultRequest = {
    outcome: 'succeeded',
    content_blocks: [{ type: 'structured_data', value }],
  }
  const snapshot = {
    outcome: 'succeeded',
    content_blocks: [{ type: 'structured_data', value: { n: 1, large: '12345678901234567890' } }],
  }
  let recovered = false
  const { client, submitted } = api((_request, body) => {
    value.n = 2
    return recovered
      ? accepted(body ?? found)
      : Response.json(
          { code: 'service_unavailable', error: 'retry later' },
          { status: 503, headers: { 'Retry-After': '1' } },
        )
  })
  const handler = vi.fn(() => Promise.resolve(result))
  const worker = createCustomToolWorker({ client, path, tools: { lookup_customer: handler } })
  const failure = worker.handle(ready).catch(toolError)
  await vi.advanceTimersByTimeAsync(5000)
  const error = await failure
  expect(error).toBeInstanceOf(CustomToolDeliveryError)
  if (!(error instanceof CustomToolDeliveryError))
    throw new Error('Expected a delivery failure', { cause: error })
  expect(error.call).toEqual({
    agentID: ready.agent_id,
    toolCallID: ready.tool_call_id,
    name: ready.name,
  })
  value.n = 3
  expect(error.result).toEqual(snapshot)
  expect(error.cause).toBeInstanceOf(ApiError)
  expect(error.retryAt).toBeTypeOf('number')
  expect(submitted).toEqual([snapshot, snapshot, snapshot])
  expect(handler).toHaveBeenCalledOnce()
  await vi.advanceTimersByTimeAsync(120_000)
  expect(submitted).toHaveLength(3)
  recovered = true
  await submitToolCallResult({
    client,
    path: { ...path, agentID: error.call.agentID, toolCallID: error.call.toolCallID },
    body: error.result,
  })
  expect(submitted).toEqual([snapshot, snapshot, snapshot, snapshot])
  expect(handler).toHaveBeenCalledOnce()
})

it.each(['conflict', 'state_transition_conflict', 'invalid_request', 'not_found'])(
  'preserves the result when submission returns %s',
  async (code) => {
    const { client, submitted } = api(() =>
      Response.json({ code, error: 'cannot accept' }, { status: code === 'not_found' ? 404 : 409 }),
    )
    const worker = createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: () => Promise.resolve(found) },
    })
    await expect(worker.handle(ready)).rejects.toMatchObject({
      name: 'CustomToolDeliveryError',
      result: found,
      cause: { code },
    })
    expect(submitted).toEqual([found])
  },
)

it('only treats the explicit terminal response as completed', async () => {
  const { client, submitted } = api(() =>
    Response.json({ code: 'tool_call_already_completed', error: 'finished' }, { status: 409 }),
  )
  const worker = createCustomToolWorker({
    client,
    path,
    tools: { lookup_customer: () => Promise.resolve(found) },
  })
  await expect(worker.handle(ready)).resolves.toEqual({ status: 'completed' })
  expect(submitted).toEqual([found])
})

it('returns every failed delivery from a check instead of dropping other computed results', async () => {
  const { client } = api((request) =>
    request.method === 'GET'
      ? Response.json({
          data: [toolCall(), toolCall(`tcl_${'b'.repeat(26)}`)],
          next_cursor: 'next',
        })
      : Response.json({ code: 'not_found', error: 'hidden' }, { status: 404 }),
  )
  const page = await createCustomToolWorker({
    client,
    path,
    tools: { lookup_customer: () => Promise.resolve(found) },
  }).check({ limit: 2 })
  expect(page.results).toHaveLength(2)
  expect(page.nextCursor).toBe('next')
  for (const { outcome } of page.results) {
    expect(outcome.status).toBe('failed')
    if (outcome.status !== 'failed') throw new Error('expected delivery failure')
    expect(outcome.error).toBeInstanceOf(CustomToolDeliveryError)
    expect(outcome.error).toMatchObject({ result: found })
  }
})

it.each(['circular', 'invalid envelope'])(
  'turns %s output into a sanitized failed result before sending',
  async (kind) => {
    interface CircularValue {
      self?: CircularValue
    }
    const invalid: SubmitToolCallResultRequest = {
      outcome: 'succeeded',
      content_blocks: [
        // @ts-expect-error deliberately missing required text in application output.
        { type: 'text' },
      ],
    }
    const circular: CircularValue = {}
    circular.self = circular
    const { client, submitted } = api((_request, body) => accepted(body ?? found))
    const onError = vi.fn()
    const worker = createCustomToolWorker({
      client,
      path,
      onError,
      tools: {
        lookup_customer: () =>
          Promise.resolve(
            kind === 'circular'
              ? {
                  outcome: 'succeeded',
                  content_blocks: [{ type: 'structured_data', value: circular }],
                }
              : invalid,
          ),
      },
    })
    await expect(worker.handle(ready)).resolves.toEqual({ status: 'completed' })
    expect(submitted).toEqual([failed()])
    expect(onError).toHaveBeenCalledOnce()
  },
)

it('hands back a late computed result after cooperative cancellation instead of losing it', async () => {
  const controller = new AbortController()
  const reason = new Error('caller finished')
  const { client, submitted } = api((_request, body) => accepted(body ?? found))
  const handler = vi.fn(() => {
    controller.abort(reason)
    return Promise.resolve(found)
  })
  const worker = createCustomToolWorker({ client, path, tools: { lookup_customer: handler } })
  await expect(worker.handle(ready, { signal: controller.signal })).rejects.toMatchObject({
    name: 'CustomToolDeliveryError',
    result: found,
    cause: reason,
  })
  expect(submitted).toEqual([])
})

it('preserves the server retry time and computed result when canceled during backoff', async () => {
  const controller = new AbortController()
  const reason = new Error('cancel retry')
  const { client, submitted } = api(() =>
    Response.json(
      { code: 'rate_limited', error: 'wait' },
      { status: 429, headers: { 'Retry-After': '5' } },
    ),
  )
  const worker = createCustomToolWorker({
    client,
    path,
    tools: { lookup_customer: () => Promise.resolve(found) },
  })
  const start = Date.now()
  const done = worker.handle(ready, { signal: controller.signal }).catch(toolError)
  await vi.advanceTimersByTimeAsync(100)
  controller.abort(reason)
  expect(await done).toMatchObject({
    name: 'CustomToolDeliveryError',
    result: found,
    cause: reason,
    retryAt: start + 5000,
  })
  await vi.advanceTimersByTimeAsync(60_000)
  expect(submitted).toEqual([found])
})

it('uses the client deadline and retry count without a separate helper budget', async () => {
  const { client, submitted } = api(() =>
    Response.json(
      { error: 'busy', code: 'service_unavailable' },
      { status: 503, headers: { 'Retry-After': '60' } },
    ),
  )
  client.setConfig({ maxRetries: 5, timeoutMs: 25 })
  const worker = createCustomToolWorker({
    client,
    path,
    tools: { lookup_customer: () => Promise.resolve(found) },
  })
  await expect(worker.handle(ready)).rejects.toMatchObject({
    name: 'CustomToolDeliveryError',
    result: found,
    cause: { status: 503 },
  })
  expect(submitted).toEqual([found])
  expect(vi.getTimerCount()).toBe(0)
})

it('keeps delivery failures explicit when the caller configures nonthrowing requests', async () => {
  const { client } = api(() =>
    Response.json({ code: 'not_found', error: 'hidden' }, { status: 404 }),
  )
  client.setConfig({ throwOnError: false })
  await expect(
    createCustomToolWorker({
      client,
      path,
      tools: { lookup_customer: () => Promise.resolve(found) },
    }).handle(ready),
  ).rejects.toMatchObject({
    name: 'CustomToolDeliveryError',
    result: found,
    cause: { status: 404 },
  })
})
