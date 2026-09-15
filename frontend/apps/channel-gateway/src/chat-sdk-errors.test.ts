import { ChatError, RateLimitError } from 'chat'
import { describe, expect, it } from 'vitest'

import { normalizeProviderDeliveryError } from './chat-sdk-errors'

describe('Chat SDK error normalization', () => {
  it('normalizes rate limits as safe retries at the adapter boundary', () => {
    expect(normalizeProviderDeliveryError(new RateLimitError('slow down', 2_500))).toMatchObject({
      outcomeUnknown: false,
      retryAfterMs: 2_500,
      retryable: true,
    })
  })

  it('normalizes definite adapter rejections as permanent failures', () => {
    expect(
      normalizeProviderDeliveryError(new ChatError('missing permission', 'PERMISSION_DENIED')),
    ).toMatchObject({ outcomeUnknown: false, retryable: false })
  })

  it('does not retry an unclassified error after a provider send starts', () => {
    expect(normalizeProviderDeliveryError(new TypeError('connection reset'))).toMatchObject({
      outcomeUnknown: true,
      retryable: false,
    })
  })
})
