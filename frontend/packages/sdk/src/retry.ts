const retryBaseDelayMs = 1_000
const retryMaximumDelayMs = 30_000
export const retryAfterMaximumDelayMs = 60_000

export function retryDelay(attempt: number): number {
  const maximum = Math.min(retryBaseDelayMs * 2 ** Math.max(attempt - 1, 0), retryMaximumDelayMs)
  return Math.floor(Math.random() * maximum)
}

export function abortableDelay(ms: number, signal?: AbortSignal): Promise<boolean> {
  if (signal?.aborted === true) return Promise.resolve(false)
  return new Promise((resolve) => {
    const onAbort = () => {
      finish(false)
    }
    const timeout = globalThis.setTimeout(() => {
      finish(true)
    }, ms)
    function finish(completed: boolean) {
      globalThis.clearTimeout(timeout)
      signal?.removeEventListener('abort', onAbort)
      resolve(completed)
    }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}
