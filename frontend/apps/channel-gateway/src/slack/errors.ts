import { ProviderDeliveryError } from '../types'

export const transientCodes = new Set([
  'internal_error',
  'fatal_error',
  'service_unavailable',
  'request_timeout',
])
export const publicCodes = new Set([
  'invalid_auth',
  'not_authed',
  'token_revoked',
  'account_inactive',
  'missing_scope',
  'not_allowed_token_type',
  'not_in_channel',
  'channel_not_found',
  'thread_not_found',
  'no_permission',
  'restricted_action',
  'is_archived',
  'msg_too_long',
  'file_not_found',
  'file_type_not_allowed',
  'invalid_arguments',
  'ratelimited',
  'already_reacted',
  'message_not_found',
])

export class SlackAPIError extends ProviderDeliveryError {
  constructor(
    readonly code: string,
    options: { retryable?: boolean; outcomeUnknown?: boolean; retryAfterMs?: number } = {},
  ) {
    super(`Slack operation failed: ${code}`, options)
    this.name = 'SlackAPIError'
  }
}
