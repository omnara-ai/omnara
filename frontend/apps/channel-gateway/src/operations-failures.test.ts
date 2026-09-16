import { describe, expect, it, vi } from 'vitest'

import type { OperationExecutionResult, OperationsOptions } from './operations'
import { auth, envelope, start } from './operations-test-support'

describe('channel operation fixed diagnostics', () => {
  it.each(['invalid_address', 'address_unavailable', 'unsupported_address'] as const)(
    'preserves a failed %s diagnostic but never claims a definite unknown result',
    async (code) => {
      for (const outcome of ['failed', 'unknown'] as const) {
        const execute = vi
          .fn<OperationsOptions['execute']>()
          .mockResolvedValue({ outcome, payload: { code } })
        const response = await invoke(execute)
        const expected: OperationExecutionResult & { request_id: string } = {
          request_id: 'request-1',
          outcome,
        }
        if (outcome === 'failed') expected.payload = { code }
        expect(await response.json()).toEqual(expected)
        expect(execute).toHaveBeenCalledOnce()
      }
    },
  )
  it.each([
    { code: 'address_unavailable', detail: 'private provider diagnostics' },
    { code: 'address_unavailable', metadata: { token: 'private token' } },
    { code: 'address_unavailable', metadata: {} },
    { code: 'untrusted_native_error' },
  ])('drops undeclared diagnostics without changing the outcome: %j', async (payload) => {
    const result: OperationExecutionResult = { outcome: 'failed' }
    Object.assign(result, { payload })
    const execute = vi.fn<OperationsOptions['execute']>().mockResolvedValue(result)
    const response = await invoke(execute)
    expect(await response.json()).toEqual({ request_id: 'request-1', outcome: 'failed' })
    expect(execute).toHaveBeenCalledOnce()
  })
})

async function invoke(execute: OperationsOptions['execute']): Promise<Response> {
  const { url } = await start(execute)
  const response = await fetch(url, {
    method: 'POST',
    headers: { ...auth, 'content-type': 'application/json' },
    body: JSON.stringify(envelope()),
  })
  expect(response.status).toBe(200)
  return response
}
