import { CliInputError } from './output.ts'

const POLL_INTERVAL_SECONDS = 2
const MAX_CONSECUTIVE_POLL_FAILURES = 3

export function sleepSeconds(seconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, seconds * 1000))
}

export async function pollUntilDeadline<T>(options: {
  expiresAt: string
  expiredMessage: string
  fetchOnce: () => Promise<T | undefined>
}): Promise<T> {
  const deadline = Date.parse(options.expiresAt)
  let consecutiveFailures = 0
  while (true) {
    try {
      const found = await options.fetchOnce()
      consecutiveFailures = 0
      if (found !== undefined) return found
    } catch (error) {
      consecutiveFailures += 1
      if (consecutiveFailures >= MAX_CONSECUTIVE_POLL_FAILURES) throw error
    }
    const remainingSeconds = (deadline - Date.now()) / 1000
    if (remainingSeconds <= 0) break
    await sleepSeconds(Math.min(POLL_INTERVAL_SECONDS, remainingSeconds))
  }
  throw new CliInputError(options.expiredMessage)
}
