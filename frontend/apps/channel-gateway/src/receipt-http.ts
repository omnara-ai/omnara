import { raceWithAbort } from './async'
import { parseObjectFields } from './operations-json'
import type { ProviderWorkReservation } from './types'

// Go accepts at most 24 MiB of normalized event JSON. The remainder covers the
// scoped receipt/lease envelope and bounded last_error metadata.
export const maxReceiptResponseBytes = 24 * 1024 * 1024 + 64 * 1024
// Small admission allowance; the reservation grows with actual response bytes.
export const initialReceiptWorkBytes = 64 * 1024

export type ReceiptClientFailureCode =
  | 'aborted'
  | 'invalid_request'
  | 'invalid_response'
  | 'response_too_large'
  | 'http_error'
  | 'transport_failed'

/** Fixed diagnostics; never retains request headers, receipt payload or causes. */
export class ReceiptClientError extends Error {
  constructor(
    readonly code: ReceiptClientFailureCode,
    readonly status?: number,
    readonly apiCode?: 'managed_work_admission_denied',
  ) {
    super(`channel receipt request failed: ${code}`)
    this.name = 'ReceiptClientError'
  }
}

/** Bound bytes before the generated SDK decodes JSON (including error bodies).
 * A claim reservation grows before parsing as bytes arrive: JSON containers,
 * strings, SDK validation and transient body copies need more than wire bytes.
 * Capacity rejection cancels intake and leaves the unknown claim to its lease;
 * it never starts behavior or issues a second hidden claim.
 */
export function receiptFetch(
  fetch: typeof globalThis.fetch,
  limit: number,
  signal: AbortSignal,
  work?: ProviderWorkReservation,
): typeof globalThis.fetch {
  return async (input, init) => {
    const response = await fetch(input, init)
    // Preserve only the fixed admission-denial code needed for a useful bot
    // response. Arbitrary server diagnostics never cross this boundary.
    if (!response.ok) {
      const code = await admissionFailureCode(response, signal)
      return Response.json(code ? { code, error: 'Managed work is not allowed.' } : {}, {
        status: response.status,
      })
    }
    const reader = response.body?.getReader()
    if (!reader) return response
    const chunks: Uint8Array[] = []
    let bytes = 0
    let ended = false
    try {
      const declared = response.headers.get('content-length')
      if (declared && /^\d+$/.test(declared) && BigInt(declared) > BigInt(limit)) {
        throw new ReceiptClientError('response_too_large')
      }
      for (;;) {
        signal.throwIfAborted()
        const next = await raceWithAbort(reader.read(), signal)
        if (next.done) {
          ended = true
          break
        }
        bytes += next.value.byteLength
        if (bytes > limit) throw new ReceiptClientError('response_too_large')
        // Conservative allocation allowance for dense JSON, not a fixed
        // reservation of the worst case for every small receipt.
        work?.resize(Math.max(initialReceiptWorkBytes, bytes * 32))
        chunks.push(next.value)
      }
      return new Response(Buffer.concat(chunks, bytes), {
        status: response.status,
        headers: response.headers,
      })
    } finally {
      if (ended) reader.releaseLock()
      else void reader.cancel().catch(() => undefined)
    }
  }
}

async function admissionFailureCode(
  response: Response,
  signal: AbortSignal,
): Promise<'managed_work_admission_denied' | undefined> {
  const reader = response.body?.getReader()
  if (!reader) return undefined
  try {
    if (response.status !== 409) return undefined
    const chunks: Uint8Array[] = []
    let bytes = 0
    for (;;) {
      const next = await raceWithAbort(reader.read(), signal)
      if (next.done) break
      bytes += next.value.byteLength
      if (bytes > 4096) return undefined
      chunks.push(next.value)
    }
    const fields = parseObjectFields(Buffer.concat(chunks, bytes).toString('utf8'), 4096)
    if (JSON.parse(fields.get('code') ?? 'null') === 'managed_work_admission_denied')
      return 'managed_work_admission_denied'
    return undefined
  } catch {
    return undefined
  } finally {
    void reader.cancel().catch(() => undefined)
  }
}
