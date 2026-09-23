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

  it('leaves the digest absent for other conflicts', () => {
    const error = ApiError.fromBody(409, { code: 'conflict', error: 'File limit reached' })
    expect(error.currentDigest).toBeUndefined()
    expect(error.message).toBe('File limit reached')
  })
})
