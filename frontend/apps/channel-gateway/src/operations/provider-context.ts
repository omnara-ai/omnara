import { AsyncLocalStorage } from 'node:async_hooks'

import type { OperationAttemptContext } from './retry'

const operations = new AsyncLocalStorage<OperationAttemptContext>()

/** SDK hooks run below the provider API's call signature. Keep their deadline
 * and cancellation local to this operation, including concurrent calls sharing
 * an adapter. This carries no credentials or routing authority.
 */
export async function withProviderOperation<T>(
  context: OperationAttemptContext,
  work: () => Promise<T>,
): Promise<T> {
  context.signal.throwIfAborted()
  const remaining = context.deadlineMs - Date.now()
  if (!Number.isSafeInteger(remaining) || remaining <= 0 || remaining > 2_147_483_647)
    throw new Error('provider operation deadline exceeded')
  const controller = new AbortController()
  const timer = setTimeout(() => {
    controller.abort()
  }, remaining)
  try {
    return await operations.run(
      { ...context, signal: AbortSignal.any([context.signal, controller.signal]) },
      work,
    )
  } finally {
    clearTimeout(timer)
    controller.abort()
  }
}

export function currentProviderOperation(): OperationAttemptContext {
  const context = operations.getStore()
  if (!context) throw new Error('provider request outside an operation')
  context.signal.throwIfAborted()
  return context
}
