import { parseRetryAfter } from '../operations/retry'

// Inspect only bounded, decoded error fields; never retain native messages in
// diagnostics. Zero remaining alone may mean a successful final allowance.
// https://github.com/octokit/plugin-throttling.js/blob/main/src/index.ts
export function githubGraphQLRateLimit(
  errors: readonly { type?: string; message?: string }[],
  headers: Headers,
): 'primary' | 'secondary' | undefined {
  if (errors.length === 0) return undefined
  if (errors.some((error) => /\bsecondary rate\b/i.test(error.message ?? ''))) return 'secondary'
  if (
    headers.get('x-ratelimit-remaining') === '0' ||
    errors.some((error) => error.type === 'RATE_LIMITED')
  )
    return 'primary'
  return undefined
}

// GitHub requires waiting for Retry-After/reset, or at least one minute for a
// secondary limit without guidance. Core owns scheduling; native clients never
// sleep/retry here. Finite milliseconds match receipt completion's one-day cap.
// https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api#exceeding-the-rate-limit
export function githubRateLimitDelay(
  headers: Headers,
  nowMs = Date.now(),
  primary = false,
): number {
  const after = parseRetryAfter(headers.get('retry-after'), nowMs)
  const exhausted = headers.get('x-ratelimit-remaining') === '0'
  const rawReset = headers.get('x-ratelimit-reset') ?? ''
  // Typed primary errors can omit remaining quota. Secondary limits with quota
  // left must not inherit an unrelated, potentially hour-long primary reset.
  const reset =
    (exhausted || primary) && /^[0-9]+$/.test(rawReset) ? Number(rawReset) * 1000 : undefined
  const delay = Math.max(after ?? 60_000, reset === undefined ? 0 : reset - nowMs, 0)
  return Math.min(86_400_000, delay)
}
