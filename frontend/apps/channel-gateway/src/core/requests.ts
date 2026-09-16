import { ApiError } from '@omnara/sdk'

import { abortableDelay, equalJitterMilliseconds } from '../async'
import { ReceiptClientError } from './receipt-http'

export async function retryCoreRequest<T>(
  signal: AbortSignal,
  request: () => Promise<T>,
  random: () => number,
): Promise<T> {
  const delays = [0, 100, 250, 500, 1_000]
  let lastError: unknown
  for (const delay of delays) {
    if (delay > 0 && !(await abortableDelay(equalJitterMilliseconds(delay, random), signal))) {
      throw abortReason(signal)
    }
    try {
      return await request()
    } catch (error) {
      lastError = error
      if (!isTransientCoreError(error)) throw error
    }
  }
  throw lastError
}

export function requireData<T>(value: T | undefined): T {
  if (value === undefined) throw new Error('Omnara API returned no response data')
  return value
}

export function receiptFailure(cause: unknown, signal: AbortSignal): ReceiptClientError {
  if (cause instanceof ReceiptClientError) return cause
  if (signal.aborted) return new ReceiptClientError('aborted')
  if (cause instanceof ApiError)
    return new ReceiptClientError(
      'http_error',
      cause.status,
      cause.code === 'managed_work_admission_denied' ? cause.code : undefined,
    )
  return new ReceiptClientError('transport_failed')
}

export function isTransientCoreError(cause: unknown): boolean {
  if (cause instanceof ApiError) {
    return cause.status === 429 || cause.status >= 500
  }
  return (
    cause instanceof TypeError || (cause instanceof DOMException && cause.name === 'TimeoutError')
  )
}

export function isCoreNotFoundError(cause: unknown): cause is ApiError {
  return cause instanceof ApiError && cause.status === 404
}

function abortReason(signal: AbortSignal): Error {
  return signal.reason instanceof Error
    ? signal.reason
    : new Error('Omnara API request was aborted', { cause: signal.reason })
}
