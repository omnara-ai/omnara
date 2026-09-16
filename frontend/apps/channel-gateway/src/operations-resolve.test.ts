import { describe, expect, it, vi } from 'vitest'

import { OperationRetryError } from './operation-retry'
import type { OperationExecutionResult, OperationsOptions } from './operations'
import { auth, envelope, part, receiveMultipart, start } from './operations-test-support'

function resolveEnvelope() {
  const value = envelope()
  return {
    ...value,
    kind: 'resolve_address',
    scope: {
      project_id: value.scope.project_id,
      integration_app_id: value.scope.integration_app_id,
      integration_install_id: value.scope.integration_install_id,
    },
    payload: { provider_ref: 'C123' },
  }
}

describe('resolve_address transport', () => {
  it('dispatches with installation scope and no artifacts', async () => {
    const execute = vi.fn<OperationsOptions['execute']>().mockResolvedValue({
      outcome: 'completed',
      payload: { definition_id: 'definition', provider_ref: 'C123', provider_ref_kind: 'channel' },
    })
    const { url } = await start(execute)
    const response = await fetch(url, {
      method: 'POST',
      headers: { ...auth, 'content-type': 'application/json' },
      body: JSON.stringify(resolveEnvelope()),
    })
    expect(response.status).toBe(200)
    expect(await response.json()).toMatchObject({ request_id: 'request-1', outcome: 'completed' })
    expect(execute).toHaveBeenCalledOnce()
    expect(execute.mock.calls[0]?.[0].scope).toEqual(resolveEnvelope().scope)
    expect(execute.mock.calls[0]?.[1]).toEqual([])
  })

  it.each(['throw', 'unknown', 'retry-error'] as const)(
    'reports %s as failed for a read-only resolve',
    async (failure) => {
      const execute = vi.fn<OperationsOptions['execute']>().mockImplementation(() => {
        if (failure === 'unknown') return Promise.resolve({ outcome: 'unknown' })
        return Promise.reject(
          failure === 'retry-error'
            ? new OperationRetryError('outcome_unknown', true, 1)
            : new Error('private provider diagnostic'),
        )
      })
      const { url } = await start(execute)
      const response = await fetch(url, {
        method: 'POST',
        headers: { ...auth, 'content-type': 'application/json' },
        body: JSON.stringify(resolveEnvelope()),
      })
      expect(await response.json()).toEqual({ request_id: 'request-1', outcome: 'failed' })
      expect(execute).toHaveBeenCalledOnce()
    },
  )

  it.each(['invalid_address', 'address_unavailable', 'unsupported_address'] as const)(
    'preserves only the fixed resolver failure %s',
    async (code) => {
      const execute = vi.fn<OperationsOptions['execute']>().mockResolvedValue({
        outcome: 'failed',
        payload: { code },
      })
      const { url } = await start(execute)
      const response = await fetch(url, {
        method: 'POST',
        headers: { ...auth, 'content-type': 'application/json' },
        body: JSON.stringify(resolveEnvelope()),
      })
      expect(response.status).toBe(200)
      expect(await response.json()).toEqual({
        request_id: 'request-1',
        outcome: 'failed',
        payload: { code },
      })
      expect(execute).toHaveBeenCalledOnce()
    },
  )

  it.each([
    { code: 'private provider error' },
    { code: 'review_not_owned' },
    { code: 'invalid_address', detail: 'private provider error' },
    { code: 'invalid_address', metadata: { review_id: 'private review' } },
    { code: 'invalid_address', metadata: {} },
    { code: 'invalid_address', extra: null },
    null,
  ])('discards malformed or non-resolver failure payloads: %j', async (payload) => {
    const result: OperationExecutionResult = { outcome: 'failed' }
    // Model a callback crossing a runtime boundary without TypeScript validation.
    Object.assign(result, { payload })
    const execute = vi.fn<OperationsOptions['execute']>().mockResolvedValue(result)
    const { url } = await start(execute)
    const response = await fetch(url, {
      method: 'POST',
      headers: { ...auth, 'content-type': 'application/json' },
      body: JSON.stringify(resolveEnvelope()),
    })
    expect(await response.json()).toEqual({ request_id: 'request-1', outcome: 'failed' })
  })

  it('does not promote an unknown outcome into a fixed address diagnosis', async () => {
    const result: OperationExecutionResult = {
      outcome: 'unknown',
      payload: { code: 'address_unavailable' },
    }
    const { url } = await start(() => Promise.resolve(result))
    const response = await fetch(url, {
      method: 'POST',
      headers: { ...auth, 'content-type': 'application/json' },
      body: JSON.stringify(resolveEnvelope()),
    })
    expect(await response.json()).toEqual({ request_id: 'request-1', outcome: 'failed' })
  })

  it.each([
    { metadata: { review_id: 'owned-review', commit_id: 'a'.repeat(40) }, valid: true },
    { metadata: { review_id: 'owned-review', extra: 'private provider error' }, valid: false },
    { metadata: { commit_id: 'not a commit' }, valid: false },
  ])(
    'strictly preserves generated failure metadata for other operations: %j',
    async ({ metadata, valid }) => {
      const payload = { code: 'review_commit_mismatch' as const, metadata }
      const { url } = await start(() => Promise.resolve({ outcome: 'failed', payload }))
      const response = await fetch(url, {
        method: 'POST',
        headers: { ...auth, 'content-type': 'application/json' },
        body: JSON.stringify(envelope()),
      })
      expect(await response.json()).toEqual(
        valid
          ? { request_id: 'request-1', outcome: 'failed', payload }
          : { request_id: 'request-1', outcome: 'failed' },
      )
    },
  )

  it('rejects even an undeclared multipart artifact before resolve dispatch', async () => {
    const execute = vi.fn<OperationsOptions['execute']>()
    const response = await receiveMultipart(
      execute,
      Buffer.concat([
        part('operation', JSON.stringify(resolveEnvelope())),
        part('artifact', 'secret content', 'a.txt', 'text/plain'),
        Buffer.from('--boundary--\r\n'),
      ]),
    )
    expect(response.status).toBe(400)
    expect(execute).not.toHaveBeenCalled()
  })
})
