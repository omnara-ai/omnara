import { ChatError, RateLimitError } from 'chat'

import { errorMessage } from './diagnostics'
import { ProviderDeliveryError } from './types'

export function normalizeProviderDeliveryError(cause: unknown): ProviderDeliveryError {
  if (cause instanceof ProviderDeliveryError) return cause
  if (cause instanceof RateLimitError) {
    return new ProviderDeliveryError(cause.message, {
      retryAfterMs: cause.retryAfterMs,
      retryable: true,
    })
  }
  if (cause instanceof ChatError || isAdapterError(cause)) {
    const code = cause.code
    if (code === 'RATE_LIMITED') {
      return new ProviderDeliveryError(errorMessage(cause), {
        retryAfterMs: adapterRetryAfterMs(cause),
        retryable: true,
      })
    }
    if (code === 'NETWORK_ERROR') {
      return new ProviderDeliveryError(errorMessage(cause), { outcomeUnknown: true })
    }
    return new ProviderDeliveryError(errorMessage(cause))
  }
  return new ProviderDeliveryError(errorMessage(cause), { outcomeUnknown: true })
}

function isAdapterError(error: unknown): error is { code: string } {
  return (
    typeof error === 'object' &&
    error !== null &&
    !Array.isArray(error) &&
    'code' in error &&
    typeof error.code === 'string'
  )
}

function adapterRetryAfterMs(error: { code: string }): number | undefined {
  if (!('retryAfterMs' in error)) return undefined
  const value = error.retryAfterMs
  return isRetryAfterMs(value) ? value : undefined
}

function isRetryAfterMs(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
}
