import { expect, it } from 'vitest'

import { currentProviderOperation, withProviderOperation } from './provider-context'

it('isolates concurrent SDK hooks and removes the context after completion', async () => {
  const controller = new AbortController()
  const context = {
    attempt: 1,
    requestId: 'one',
    deadlineMs: Date.now() + 10_000,
    signal: controller.signal,
  }
  let release!: () => void
  const waiting = new Promise<void>((resolve) => {
    release = resolve
  })
  const first = withProviderOperation(context, async () => {
    const signal = currentProviderOperation().signal
    await waiting
    expect(currentProviderOperation().requestId).toBe('one')
    controller.abort()
    expect(signal.aborted).toBe(true)
    expect(() => currentProviderOperation()).toThrow(controller.signal.reason)
  })
  await withProviderOperation(
    { ...context, requestId: 'two', signal: new AbortController().signal },
    async () => {
      expect(currentProviderOperation().requestId).toBe('two')
      release()
      await first
      expect(currentProviderOperation().requestId).toBe('two')
      expect(currentProviderOperation().signal.aborted).toBe(false)
    },
  )
  expect(() => currentProviderOperation()).toThrow('outside an operation')
})

it('aborts a provider request at the deadline even outside retryOperation', async () => {
  const context = {
    attempt: 1,
    requestId: 'deadline',
    deadlineMs: Date.now() + 20,
    signal: new AbortController().signal,
  }
  await withProviderOperation(context, async () => {
    const signal = currentProviderOperation().signal
    await new Promise<void>((resolve) => {
      signal.addEventListener(
        'abort',
        () => {
          resolve()
        },
        { once: true },
      )
    })
    expect(signal.aborted).toBe(true)
  })
  expect(() => currentProviderOperation()).toThrow('outside an operation')
})
