export async function abortableDelay(milliseconds: number, signal?: AbortSignal): Promise<boolean> {
  if (signal?.aborted) return false
  return new Promise<boolean>((resolve) => {
    let timer: ReturnType<typeof setTimeout> | undefined
    const finish = (elapsed: boolean): void => {
      if (timer === undefined) return
      clearTimeout(timer)
      timer = undefined
      signal?.removeEventListener('abort', onAbort)
      resolve(elapsed)
    }
    const onAbort = (): void => {
      finish(false)
    }
    timer = setTimeout(() => {
      finish(true)
    }, milliseconds)
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

export function equalJitterMilliseconds(
  milliseconds: number,
  random: () => number = Math.random,
): number {
  const fraction = clampedRandom(random)
  return Math.floor(milliseconds / 2 + (milliseconds / 2) * fraction)
}

export function pollJitterMilliseconds(
  milliseconds: number,
  random: () => number = Math.random,
): number {
  const fraction = clampedRandom(random)
  return Math.round(milliseconds * (0.9 + 0.2 * fraction))
}

function clampedRandom(random: () => number): number {
  return Math.min(Math.max(random(), 0), 1)
}

export function asError(cause: unknown): Error {
  return cause instanceof Error ? cause : new Error(String(cause))
}

export function abortError(signal: AbortSignal): Error {
  return signal.reason instanceof Error ? signal.reason : new Error('Operation was aborted')
}

export async function raceWithAbort<T>(work: Promise<T>, signal: AbortSignal): Promise<T> {
  if (signal.aborted) {
    // The caller may already have started work; observe a later rejection even
    // when cancellation wins before we attach the normal completion handlers.
    void work.catch(() => undefined)
    throw abortError(signal)
  }
  return new Promise<T>((resolve, reject) => {
    const onAbort = (): void => {
      reject(abortError(signal))
    }
    signal.addEventListener('abort', onAbort, { once: true })
    work.then(
      (value) => {
        signal.removeEventListener('abort', onAbort)
        resolve(value)
      },
      (cause: unknown) => {
        signal.removeEventListener('abort', onAbort)
        reject(asError(cause))
      },
    )
  })
}
