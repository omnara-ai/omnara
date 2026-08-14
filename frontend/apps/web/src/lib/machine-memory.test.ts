import { describe, expect, it } from 'vitest'

import {
  formatMemoryGb,
  memoryGbDraft,
  memoryGbDraftValid,
  memoryGbToMb,
  memoryGbToMbPreservingOriginal,
} from './machine-memory'

describe('machine memory units', () => {
  it.each([
    [null, undefined],
    [0, '0 GB'],
    [1, '<0.01 GB'],
    [1024, '1 GB'],
    [1536, '1.5 GB'],
    [9000, '8.79 GB'],
  ])('formats %s MB as %s', (memoryMb, expected) => {
    expect(formatMemoryGb(memoryMb)).toBe(expected)
  })

  it('formats concise input drafts without making small or maximum values invalid', () => {
    expect(memoryGbDraft(null)).toBe('')
    expect(memoryGbDraft(1024)).toBe('1')
    expect(memoryGbDraft(9000)).toBe('8.79')
    expect(memoryGbDraft(1)).toBe('0.0009765625')
    expect(memoryGbDraft(2_147_483_647)).toBe('2097151.9990234375')
  })

  it('validates GB drafts against the integer MB API range', () => {
    expect(memoryGbDraftValid('1.25')).toBe(true)
    expect(memoryGbDraftValid('')).toBe(false)
    expect(memoryGbDraftValid('', { optional: true })).toBe(true)
    expect(memoryGbDraftValid('0')).toBe(false)
    expect(memoryGbDraftValid('0', { allowZero: true })).toBe(true)
    expect(memoryGbDraftValid('0.0001', { allowZero: true })).toBe(false)
    expect(memoryGbDraftValid('-1', { allowZero: true })).toBe(false)
    expect(memoryGbDraftValid('2097152')).toBe(false)
  })

  it('converts GB drafts to integer MB and preserves untouched rounded values', () => {
    expect(memoryGbToMb('1.25')).toBe(1280)
    expect(memoryGbToMb('8.79')).toBe(9001)
    expect(memoryGbToMbPreservingOriginal('8.79', 9000)).toBe(9000)
    expect(memoryGbToMbPreservingOriginal('8.5', 9000)).toBe(8704)
  })
})
