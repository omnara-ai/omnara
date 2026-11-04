import { CliInputError } from './output.ts'

const POLL_INTERVAL_SECONDS = 2
const MAX_CONSECUTIVE_POLL_FAILURES = 3
// setTimeout overflows past this many milliseconds and fires immediately.
const MAX_TIMER_MS = 2 ** 31 - 1

export function sleepSeconds(seconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, seconds * 1000))
}

class DeadlineExceeded extends Error {}

// Thrown from fetchOnce when the target can never match, so polling stops
// at once instead of retrying until the deadline.
export class PollTerminalError extends CliInputError {}

// Runs one fetch, aborting it and rejecting with DeadlineExceeded once abortAt
// passes, even if the fetch ignores its abort signal.
async function fetchBeforeDeadline<T>(
  abortAt: number,
  fetchOnce: (signal: AbortSignal) => Promise<T | undefined>,
): Promise<T | undefined> {
  const controller = new AbortController()
  const remainingMs = abortAt - Date.now()
  let timer: ReturnType<typeof setTimeout> | undefined
  const expired = new Promise<never>((_, reject) => {
    if (remainingMs > MAX_TIMER_MS) return
    timer = setTimeout(
      () => {
        controller.abort()
        reject(new DeadlineExceeded())
      },
      Math.max(0, remainingMs),
    )
  })
  try {
    return await Promise.race([fetchOnce(controller.signal), expired])
  } finally {
    clearTimeout(timer)
  }
}

export async function pollUntilDeadline<T>(options: {
  expiresAt: string
  expiredMessage: string
  fetchOnce: (signal: AbortSignal) => Promise<T | undefined>
}): Promise<T> {
  const deadline = Date.parse(options.expiresAt)
  // The last poll starts at the deadline, so give every request one poll
  // interval past the deadline to land and still count as a match.
  const abortAt = deadline + POLL_INTERVAL_SECONDS * 1000
  let consecutiveFailures = 0
  while (true) {
    try {
      const found = await fetchBeforeDeadline(abortAt, options.fetchOnce)
      consecutiveFailures = 0
      if (found !== undefined) return found
    } catch (error) {
      if (error instanceof PollTerminalError) throw error
      if (error instanceof DeadlineExceeded || Date.now() >= deadline) break
      consecutiveFailures += 1
      if (consecutiveFailures >= MAX_CONSECUTIVE_POLL_FAILURES) throw error
    }
    const remainingSeconds = (deadline - Date.now()) / 1000
    if (remainingSeconds <= 0) break
    await sleepSeconds(Math.min(POLL_INTERVAL_SECONDS, remainingSeconds))
  }
  throw new CliInputError(options.expiredMessage)
}
