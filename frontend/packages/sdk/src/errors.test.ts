import { describe, expect, it } from 'vitest'

import { ApiError } from './errors'

describe('ApiError', () => {
  it('preserves unknown error codes', () => {
    const error = ApiError.fromBody(409, { code: 'future_conflict', error: 'Future conflict' })
    expect(error.code).toBe('future_conflict')
    expect(error.message).toBe('Future conflict')
  })

  it('preserves the current digest for a file-content conflict', async () => {
    const digest = `sha256:${'a'.repeat(64)}`
    const error = await ApiError.fromResponse(
      Response.json(
        { code: 'file_content_conflict', error: 'File changed', current_digest: digest },
        { status: 409 },
      ),
    )
    expect(error.code).toBe('file_content_conflict')
    expect(error.currentDigest).toBe(digest)
    expect(error.message).toBe('File changed')
  })

  it('reads Retry-After as milliseconds', async () => {
    const limited = await ApiError.fromResponse(
      Response.json(
        { code: 'rate_limited', error: 'too many requests: retry after 1800s' },
        { status: 429, headers: { 'Retry-After': '1800' } },
      ),
    )
    expect(limited.retryAfterMs).toBe(1_800_000)
    const conflict = await ApiError.fromResponse(
      Response.json({ code: 'conflict', error: 'Conflict' }, { status: 409 }),
    )
    expect(conflict.retryAfterMs).toBeUndefined()
  })

  it('leaves the digest absent for other conflicts', () => {
    const error = ApiError.fromBody(409, { code: 'conflict', error: 'File limit reached' })
    expect(error.currentDigest).toBeUndefined()
    expect(error.message).toBe('File limit reached')
  })
})
