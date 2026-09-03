import { describe, expect, it } from 'vitest'

import { equalJitterMilliseconds, pollJitterMilliseconds } from './async'

describe('channel gateway timing jitter', () => {
  it('uses equal jitter for retries', () => {
    expect(equalJitterMilliseconds(100, () => 0)).toBe(50)
    expect(equalJitterMilliseconds(100, () => 0.5)).toBe(75)
    expect(equalJitterMilliseconds(100, () => 1)).toBe(100)
  })

  it('uses bounded symmetric jitter for polling', () => {
    expect(pollJitterMilliseconds(100, () => 0)).toBe(90)
    expect(pollJitterMilliseconds(100, () => 0.5)).toBe(100)
    expect(pollJitterMilliseconds(100, () => 1)).toBe(110)
  })
})
