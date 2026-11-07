const retryBaseDelayMs = 1_000
const retryMaximumDelayMs = 30_000
const timerMaximumDelayMs = 2_147_483_647
export const retryAfterMaximumDelayMs = 60_000

export function retryDelay(attempt: number): number {
  const maximum = Math.min(retryBaseDelayMs * 2 ** Math.max(attempt - 1, 0), retryMaximumDelayMs)
  return Math.floor(Math.random() * maximum)
}

export function abortableDelay(ms: number, signal?: AbortSignal): Promise<boolean> {
  if (signal?.aborted === true) return Promise.resolve(false)
  return new Promise((resolve) => {
    let remaining = ms
    let timeout: ReturnType<typeof globalThis.setTimeout>
    function schedule() {
      // Larger delays overflow runtime timers. Preserve the full wait across bounded chunks.
      const chunk = Math.min(remaining, timerMaximumDelayMs)
      timeout = globalThis.setTimeout(() => {
        remaining -= chunk
        if (remaining > 0) schedule()
        else finish(true)
      }, chunk)
    }
    const onAbort = () => {
      finish(false)
    }
    function finish(completed: boolean) {
      globalThis.clearTimeout(timeout)
      signal?.removeEventListener('abort', onAbort)
      resolve(completed)
    }
    signal?.addEventListener('abort', onAbort, { once: true })
    schedule()
  })
}
