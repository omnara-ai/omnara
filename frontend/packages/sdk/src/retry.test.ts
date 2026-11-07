import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { trackAbortListeners } from './abort-test-support'
import { abortableDelay } from './retry'

const maximumTimerMs = 2_147_483_647

beforeEach(() => vi.useFakeTimers())
afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

it('waits the full oversized delay with one timer and one caller listener', async () => {
  const source = new AbortController()
  const listeners = trackAbortListeners(source.signal)
  const settled = vi.fn()
  const done = abortableDelay(maximumTimerMs + 20, source.signal).then(settled)
  await vi.advanceTimersByTimeAsync(maximumTimerMs)
  expect(settled).not.toHaveBeenCalled()
  expect(vi.getTimerCount()).toBe(1)
  expect(listeners.size).toBe(1)
  await vi.advanceTimersByTimeAsync(19)
  expect(settled).not.toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(1)
  await done
  expect(settled).toHaveBeenCalledExactlyOnceWith(true)
  expect(vi.getTimerCount()).toBe(0)
  expect(listeners.size).toBe(0)
})

it.each([0, maximumTimerMs])('cleans up an oversized wait aborted after %i ms', async (elapsed) => {
  const source = new AbortController()
  const listeners = trackAbortListeners(source.signal)
  const done = abortableDelay(maximumTimerMs + 20, source.signal)
  await vi.advanceTimersByTimeAsync(elapsed)
  expect(listeners.size).toBe(1)
  source.abort()
  await expect(done).resolves.toBe(false)
  expect(vi.getTimerCount()).toBe(0)
  expect(listeners.size).toBe(0)
})
