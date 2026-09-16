import type {
  ChannelOperationFailure,
  ChannelRegistrationTarget,
  ChannelResolveAddressOperation,
} from '@omnara/sdk'

import type { CoreClient } from '../core-client'
import {
  type OperationAttemptContext,
  type OperationRetryOptions,
  retryOperation,
} from '../operation-retry'
import { slackDefinition } from './behavior'
import { SlackAPIError, type SlackClient } from './client'
import { slackTimestamp } from './protocol'

interface SlackAddressContext {
  installation: Parameters<CoreClient['publishDefinition']>[0]
  teamId: string
  publishDefinition: CoreClient['publishDefinition']
}

/** Fixed setup diagnostics only; never retains a provider message or cause. */
export class SlackAddressError extends Error {
  constructor(
    readonly code: Extract<
      ChannelOperationFailure['code'],
      'invalid_address' | 'address_unavailable' | 'unsupported_address'
    >,
  ) {
    super(`Slack address resolution failed: ${code}`)
  }
}

/** Resolve existing Slack addresses only. Definition publication describes
 * support; core separately owns target registration, bindings and authorization.
 */
export async function resolveSlackAddress(
  client: SlackClient,
  input: ChannelResolveAddressOperation,
  context: SlackAddressContext,
  operation: Omit<OperationRetryOptions, 'idempotent'>,
): Promise<ChannelRegistrationTarget> {
  const [channelId, timestamp, extra] = input.provider_ref.split(':')
  if (
    !channelId ||
    !/^[CGD][A-Z0-9]{1,127}$/.test(channelId) ||
    extra !== undefined ||
    (timestamp !== undefined && !slackTimestamp.safeParse(timestamp).success)
  )
    throw new SlackAddressError('invalid_address')
  if (
    input.provider_ref_kind !== undefined &&
    !['channel', 'dm', 'thread'].includes(input.provider_ref_kind)
  )
    throw new SlackAddressError('unsupported_address')
  if (!context.teamId) throw new SlackAPIError('invalid_configuration')

  const result = await retryOperation({ ...operation, idempotent: true }, async (attempt) => {
    let address
    try {
      address = await lookupAddress(
        client,
        { channelId, timestamp, kind: input.provider_ref_kind },
        context.teamId,
        attempt,
      )
    } catch (cause) {
      // Return fixed failures through the retry loop, which otherwise deliberately
      // erases provider errors. Definition publication is outside this catch.
      if (cause instanceof SlackAddressError) return cause
      if (
        cause instanceof SlackAPIError &&
        ['channel_not_found', 'thread_not_found', 'message_not_found', 'not_in_channel'].includes(
          cause.code,
        )
      )
        return new SlackAddressError('address_unavailable')
      throw cause
    }
    const { kind, providerRef, displayName } = address
    const parent =
      kind === 'thread'
        ? await context.publishDefinition(
            context.installation,
            slackDefinition('channel'),
            attempt.signal,
          )
        : undefined
    const definition = await context.publishDefinition(
      context.installation,
      slackDefinition(kind),
      attempt.signal,
    )
    return {
      definition_id: definition.id,
      provider_ref: providerRef,
      provider_ref_kind: kind,
      display_name: displayName,
      parent: parent
        ? {
            definition_id: parent.id,
            provider_ref: channelId,
            provider_ref_kind: 'channel',
            display_name: displayName,
          }
        : undefined,
    }
  })
  if (result instanceof SlackAddressError) throw result
  return result
}

async function lookupAddress(
  client: SlackClient,
  input: { channelId: string; timestamp?: string; kind?: string },
  teamId: string,
  attempt: OperationAttemptContext,
) {
  const { channelId, timestamp } = input
  const { channel } = await client.api('conversations.info', { channel: channelId }, attempt)
  if (!channel.id || channel.context_team_id === '') throw new SlackAPIError('invalid_response')
  if (
    channel.id !== channelId ||
    (channel.context_team_id !== undefined && channel.context_team_id !== teamId)
  )
    throw new SlackAddressError('address_unavailable')
  if (channel.is_mpim) throw new SlackAddressError('unsupported_address')
  const dm = channel.is_im === true
  const room = channel.is_channel === true || channel.is_group === true
  if (dm && room) throw new SlackAPIError('invalid_response')
  if (!dm && !room) throw new SlackAPIError('invalid_response')
  if (timestamp !== undefined && dm) throw new SlackAddressError('unsupported_address')
  const kind: Parameters<typeof slackDefinition>[0] =
    timestamp !== undefined ? 'thread' : dm ? 'dm' : 'channel'
  if (input.kind !== undefined && input.kind !== kind)
    throw new SlackAddressError('invalid_address')
  const name = channel.name?.trim()
  const displayName = name === '' ? undefined : name
  if (displayName && Buffer.byteLength(displayName) > 512)
    throw new SlackAPIError('invalid_response')
  let providerRef = channelId
  if (timestamp) {
    // A single root lookup proves the address; resolving never pages through history.
    const page = await client.api(
      'conversations.replies',
      { channel: channelId, ts: timestamp, limit: 1 },
      attempt,
    )
    const root = page.messages[0]
    if (
      !root ||
      !sameTimestamp(root.ts, timestamp) ||
      (root.thread_ts !== undefined && !sameTimestamp(root.thread_ts, root.ts)) ||
      (root.channel !== undefined && root.channel !== channelId)
    )
      throw new SlackAddressError('address_unavailable')
    providerRef = `${channelId}:${root.ts}`
  }
  return { kind, providerRef, displayName }
}

function sameTimestamp(left: string, right: string): boolean {
  const [leftSeconds = '', leftMicros = ''] = left.split('.')
  const [rightSeconds = '', rightMicros = ''] = right.split('.')
  return (
    BigInt(leftSeconds) === BigInt(rightSeconds) &&
    leftMicros.padEnd(6, '0') === rightMicros.padEnd(6, '0')
  )
}
