import { describe, expect, it, vi } from 'vitest'

import { serializeOperationResult } from './operations-json'

describe('operation result serialization', () => {
  it('bounds result serialization before visiting later result properties', () => {
    const late = vi.fn(() => 'never read')
    const payload = Object.defineProperty({ huge: 'x'.repeat(1024 * 1024 + 1) }, 'late', {
      enumerable: true,
      get: late,
    })
    expect(() => serializeOperationResult(payload)).toThrow('invalid channel operation')
    expect(late).not.toHaveBeenCalled()
    expect(() => serializeOperationResult({ escaped: '\u0000'.repeat(200_000) })).toThrow()
    expect(() =>
      serializeOperationResult({ nodes: Array.from({ length: 16_384 }, () => null) }),
    ).toThrow()
    expect(serializeOperationResult({ number: 1, text: 'résumé', array: [true, null] })).toBe(
      JSON.stringify({ number: 1, text: 'résumé', array: [true, null] }),
    )
  })
})
