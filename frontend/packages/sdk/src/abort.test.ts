import { afterEach, expect, it, vi } from 'vitest'

import { forwardAbort } from './abort'
import { trackAbortListeners } from './abort-test-support'

afterEach(() => vi.restoreAllMocks())

it('forwards an existing abort synchronously with the identical reason', () => {
  const source = new AbortController()
  const target = new AbortController()
  const reason = { caller: 'stopped' }
  source.abort(reason)
  const listeners = trackAbortListeners(source.signal)
  const dispose = forwardAbort(source.signal, target)
  expect(target.signal.aborted).toBe(true)
  expect(target.signal.reason).toBe(reason)
  expect(listeners.size).toBe(0)
  dispose()
})

it('disposes the forwarding listener on abort or explicit completion', () => {
  const source = new AbortController()
  const listeners = trackAbortListeners(source.signal)
  const completed = new AbortController()
  const active = new AbortController()
  const dispose = forwardAbort(source.signal, completed)
  forwardAbort(source.signal, active)
  expect(listeners.size).toBe(2)
  dispose()
  dispose()
  expect(listeners.size).toBe(1)
  const reason = new Error('caller stopped')
  source.abort(reason)
  expect(active.signal.reason).toBe(reason)
  expect(completed.signal.aborted).toBe(false)
  expect(listeners.size).toBe(0)
})
