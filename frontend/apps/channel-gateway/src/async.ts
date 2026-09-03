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
