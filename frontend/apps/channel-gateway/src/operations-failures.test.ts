import type { ChannelOperationFailure } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import type { OperationExecutionResult, OperationsOptions } from './operations'
import { auth, envelope, start } from './operations-test-support'

describe('channel operation recovery facts', () => {
  it.each([
    { outcome: 'failed', code: 'review_operation_unknown' },
    { outcome: 'unknown', code: 'review_commit_mismatch' },
  ] as const)(
    'drops contradictory diagnostics without reclassifying $outcome',
    async ({ outcome, code }) => {
      const execute = vi
        .fn<OperationsOptions['execute']>()
        .mockResolvedValue({ outcome, payload: { code } })
      const response = await invoke(execute)
      expect(await response.json()).toEqual({ request_id: 'request-1', outcome })
      expect(execute).toHaveBeenCalledOnce()
    },
  )
  it.each(['failed', 'unknown'] as const)(
    'preserves the known draft identity without changing %s into a safe retry',
    async (outcome) => {
      const payload: ChannelOperationFailure = {
        code: outcome === 'unknown' ? 'review_operation_unknown' : 'review_finding_failed',
        metadata: { review_id: 'PRR_recorded', commit_id: 'a'.repeat(40) },
      }
      const execute = vi.fn<OperationsOptions['execute']>().mockResolvedValue({ outcome, payload })
      const response = await invoke(execute)
      expect(await response.json()).toEqual({ request_id: 'request-1', outcome, payload })
      expect(execute).toHaveBeenCalledOnce()
    },
  )

  it.each([
    { code: 'review_finding_failed', detail: 'private provider diagnostics' },
    { code: 'review_finding_failed', metadata: { token: 'private token' } },
    { code: 'review_finding_failed', metadata: { review_id: 'a'.repeat(513) } },
    { code: 'review_finding_failed', metadata: { commit_id: 'not-a-commit' } },
    { code: 'provider_pending_review_conflict', metadata: { review_id: 'PRR_foreign' } },
    { code: 'review_not_owned', metadata: { review_id: 'PRR_foreign' } },
  ])(
    'drops untrusted or foreign recovery facts with a matching failure outcome: %j',
    async (payload) => {
      const result: OperationExecutionResult = { outcome: 'failed' }
      // Model an adapter response crossing the runtime boundary without type checks.
      Object.assign(result, { payload })
      const execute = vi.fn<OperationsOptions['execute']>().mockResolvedValue(result)
      const response = await invoke(execute)
      expect(await response.json()).toEqual({ request_id: 'request-1', outcome: 'failed' })
      expect(execute).toHaveBeenCalledOnce()
    },
  )
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
