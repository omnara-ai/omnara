/** Keep cancellation linked until the returned body ends, without buffering it. */
export function responseWithStreamLifetime(
  response: Response,
  signal: AbortSignal,
  dispose: () => void,
): Response {
  if (response.body === null) return response
  const wrapped = new Response(streamWithLifetime(response.body, signal, dispose), response)
  return copyResponseMetadata(wrapped, response)
}

function copyResponseMetadata(target: Response, source: Response): Response {
  // ResponseInit carries status and headers, but not these network response fields.
  Object.defineProperties(target, {
    url: { value: source.url },
    redirected: { value: source.redirected },
    type: { value: source.type },
    clone: {
      configurable: true,
      writable: true,
      value: function clone(this: Response): Response {
        // Native cloning owns teeing, receiver validation, and locked/used body checks.
        return copyResponseMetadata(Response.prototype.clone.call(this), this)
      },
    },
  })
  return target
}

export function streamWithLifetime(
  source: ReadableStream<Uint8Array>,
  signal: AbortSignal,
  dispose: () => void,
): ReadableStream<Uint8Array> {
  const reader = source.getReader()
  let finished = false
  let disposed = false
  let output: ReadableStreamDefaultController<Uint8Array>
  function release() {
    if (disposed) return
    disposed = true
    signal.removeEventListener('abort', abort)
    dispose()
  }
  // Stream errors and abort reasons are opaque; preserve their identity without parsing.
  // oxlint-disable-next-line anti-slop/no-unknown-parameters
  function fail(reason: unknown) {
    if (finished) return
    finished = true
    output.error(reason)
    release()
    void reader
      .cancel(reason)
      .catch(() => undefined)
      .finally(() => {
        reader.releaseLock()
      })
  }
  function abort() {
    fail(signal.reason)
  }
  return new ReadableStream<Uint8Array>(
    {
      start(controller) {
        output = controller
        if (signal.aborted) abort()
        else signal.addEventListener('abort', abort, { once: true })
        // An empty or errored body can settle before the consumer begins reading.
        // Releasing the link must not discard a final chunk already being read.
        void reader.closed.then(release, fail)
      },
      async pull(controller) {
        try {
          const chunk = await reader.read()
          if (finished) return
          if (chunk.done) {
            finished = true
            controller.close()
            release()
            reader.releaseLock()
          } else controller.enqueue(chunk.value)
        } catch (error) {
          fail(error)
        }
      },
      // oxlint-disable-next-line anti-slop/no-unknown-parameters
      async cancel(reason: unknown) {
        finished = true
        release()
        try {
          await reader.cancel(reason)
        } finally {
          reader.releaseLock()
        }
      },
    },
    { highWaterMark: 0 },
  )
}
