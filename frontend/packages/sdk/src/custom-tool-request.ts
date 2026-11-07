import type { OmnaraClient } from './client'
import { parseRetryAfter } from './errors'

export class ToolRequestError extends Error {
  constructor(
    override readonly cause: Error,
    readonly retryAt: number,
  ) {
    super(cause.message, { cause })
  }
}

export function toolError(cause: unknown): Error {
  if (cause instanceof ToolRequestError) return cause.cause
  return cause instanceof Error ? cause : new Error(String(cause))
}

/** Keep the delivery not-before time even when the caller cancels during SDK backoff. */
export async function requestToolAPI<T>(
  client: OmnaraClient,
  request: (fetch: typeof globalThis.fetch, signal: AbortSignal, timeoutMs: number) => Promise<T>,
  signal: AbortSignal,
  timeoutMs: number,
): Promise<T> {
  let retryAt = 0
  try {
    return await request(
      async (input, init) => {
        const response = await (client.getConfig().fetch ?? globalThis.fetch)(input, init)
        retryAt = Date.now() + (parseRetryAfter(response.headers.get('Retry-After')) ?? 0)
        return response
      },
      signal,
      timeoutMs,
    )
  } catch (cause) {
    throw new ToolRequestError(toolError(cause), retryAt)
  }
}
