/** Forward cancellation for one owned operation; dispose when that operation ends. */
export function forwardAbort(
  source: AbortSignal | null | undefined,
  target: AbortController,
): () => void {
  let attached = false
  function dispose() {
    if (!attached) return
    attached = false
    source?.removeEventListener('abort', abort)
  }
  function abort() {
    dispose()
    target.abort(source?.reason)
  }
  if (source?.aborted) abort()
  else if (source) {
    attached = true
    source.addEventListener('abort', abort, { once: true })
  }
  return dispose
}

/** Race the entire SDK call, including auth and response parsing, against cancellation. */
export function untilAborted<T>(request: () => Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise((resolve, reject) => {
    const abort = () => {
      signal.removeEventListener('abort', abort)
      // Fetch preserves arbitrary caller-supplied abort reasons, including non-Errors.
      // oxlint-disable-next-line typescript/prefer-promise-reject-errors
      reject(signal.reason)
    }
    if (signal.aborted) {
      abort()
      return
    }
    signal.addEventListener('abort', abort, { once: true })
    Promise.resolve()
      .then(() => {
        signal.throwIfAborted()
        return request()
      })
      .then(resolve, reject)
      .finally(() => {
        signal.removeEventListener('abort', abort)
      })
  })
}
