import { describe, expect, it } from 'vitest'

import { equalJitterMilliseconds, pollJitterMilliseconds, raceWithAbort } from './async'

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

describe('channel gateway cancellation', () => {
  it('preserves the abort reason while observing already rejected work', async () => {
    const reason = new DOMException('Gateway shutdown', 'AbortError')
    await expect(
      raceWithAbort(Promise.reject(new Error('Late failure')), AbortSignal.abort(reason)),
    ).rejects.toBe(reason)
    // Vitest also reports any unhandled rejection from the losing work promise.
    await new Promise<void>((resolve) => setImmediate(resolve))
  })

  it('observes work that fails after cancellation wins', async () => {
    const controller = new AbortController()
    const work = Promise.resolve().then(() => {
      throw new Error('Late provider failure')
    })
    const reason = new Error('Canceled')
    const raced = raceWithAbort(work, controller.signal)
    controller.abort(reason)
    await expect(raced).rejects.toBe(reason)
    await new Promise<void>((resolve) => setImmediate(resolve))
  })

  it('returns successful work and preserves ordinary errors', async () => {
    const signal = new AbortController().signal
    await expect(raceWithAbort(Promise.resolve('ready'), signal)).resolves.toBe('ready')
    const reason = new Error('Provider failure')
    await expect(raceWithAbort(Promise.reject(reason), signal)).rejects.toBe(reason)
  })
})
