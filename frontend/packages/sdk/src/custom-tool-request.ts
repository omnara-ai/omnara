import type { OmnaraClient } from './client'
import type { CustomToolCallRef } from './custom-tool-worker-types'
import { parseRetryAfter } from './errors'
import type { SubmitToolCallResultRequest } from './generated/types.gen'

export class CustomToolDeliveryError extends Error {
  constructor(
    readonly call: CustomToolCallRef,
    readonly result: SubmitToolCallResultRequest,
    override readonly cause: unknown,
    /** Earliest retry time in epoch milliseconds, when the server supplied Retry-After. */
    readonly retryAt?: number,
  ) {
    super(`Could not deliver the result for custom tool call ${call.toolCallID}`, { cause })
    this.name = 'CustomToolDeliveryError'
  }
}

export function toolError(cause: unknown): Error {
  return cause instanceof Error ? cause : new Error(String(cause))
}

export async function deliverToolResult<T>(
  client: OmnaraClient,
  call: CustomToolCallRef,
  result: SubmitToolCallResultRequest,
  request: (fetch: typeof globalThis.fetch) => Promise<T>,
): Promise<T> {
  let retryAt: number | undefined
  try {
    return await request(async (input, init) => {
      const response = await (client.getConfig().fetch ?? globalThis.fetch)(input, init)
      const delay = parseRetryAfter(response.headers.get('Retry-After'))
      retryAt = delay === undefined ? undefined : Date.now() + delay
      return response
    })
  } catch (cause) {
    throw new CustomToolDeliveryError(call, result, cause, retryAt)
  }
}
