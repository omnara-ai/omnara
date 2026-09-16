import type {
  ChannelOperationDestination,
  ChannelOperationFailure,
  ChannelRegistrationTarget,
  ChannelResolveAddressOperation,
  PublishChannelConnectorDefinitionRequest,
} from '@omnara/sdk'

import type { CoreClient } from '../core/client'
import {
  type OperationAttemptContext,
  type OperationRetryOptions,
  retryOperation,
} from '../operations/retry'
import type { DiscordClient } from './client'
import { DiscordAPIError, discordID } from './protocol'

type AddressKind = 'channel' | 'thread'
interface DiscordDestination {
  id: string
  kind: AddressKind
}
export class DiscordAddressError extends Error {
  constructor(
    readonly code: Extract<
      ChannelOperationFailure['code'],
      'invalid_address' | 'address_unavailable' | 'unsupported_address'
    >,
  ) {
    super(`Discord address resolution failed: ${code}`)
  }
}

/** Guild text channels and their public threads only. Grants belong to core. */
export function discordDefinition(kind: AddressKind): PublishChannelConnectorDefinitionRequest {
  return {
    implementation_key: `discord_${kind}`,
    kind: kind === 'channel' ? 'DISCORD_CHANNEL' : 'DISCORD_THREAD',
    description: kind === 'channel' ? 'A Discord guild text channel.' : 'A Discord public thread.',
    send_params_schema: {
      type: 'object',
      properties:
        kind === 'channel'
          ? {
              thread_name: {
                type: 'string',
                minLength: 1,
                maxLength: 100,
                description:
                  'Name for the reply thread, when creating a reply thread is authorized.',
              },
            }
          : {},
      additionalProperties: false,
    },
    capabilities: {
      read: true,
      send: true,
      text: true,
      artifacts: true,
      permissions: false,
      questions: false,
      creates_reply_channel: kind === 'channel',
    },
  }
}

export function discordDestination(input: ChannelOperationDestination): DiscordDestination {
  if (!discordID.safeParse(input.provider_ref).success)
    throw new DiscordAPIError('invalid_destination')
  if (input.provider_ref_kind !== 'channel' && input.provider_ref_kind !== 'thread')
    throw new DiscordAPIError('unsupported_destination')
  if (input.implementation_key !== `discord_${input.provider_ref_kind}`)
    throw new DiscordAPIError('unsupported_implementation')
  return { id: input.provider_ref, kind: input.provider_ref_kind }
}

/** All resource reads use the installed guild, including a thread's real parent.
 * Neither provider response URLs nor tool metadata choose an API destination.
 */
export async function loadDiscordAddress(
  client: DiscordClient,
  id: string,
  expectedKind: AddressKind | undefined,
  attempt: OperationAttemptContext,
) {
  await client.verifyIdentity(attempt)
  const channel = await client.getChannel(id, attempt)
  if (channel.type !== 0 && channel.type !== 11)
    throw new DiscordAddressError('unsupported_address')
  if (channel.guild_id === undefined) throw new DiscordAPIError('invalid_response')
  if (channel.id !== id || channel.guild_id !== client.configuration.guildID)
    throw new DiscordAddressError('address_unavailable')
  const kind = channel.type === 0 ? 'channel' : 'thread'
  if (expectedKind !== undefined && kind !== expectedKind)
    throw new DiscordAddressError('invalid_address')
  if (kind === 'channel') return { channel, kind, parent: undefined }
  if (!channel.parent_id || channel.parent_id === id) throw new DiscordAPIError('invalid_response')
  const parent = await client.getChannel(channel.parent_id, attempt)
  if (parent.id !== channel.parent_id || parent.guild_id !== client.configuration.guildID)
    throw new DiscordAddressError('address_unavailable')
  if (parent.type !== 0) throw new DiscordAddressError('unsupported_address')
  return { channel, kind, parent }
}

export async function resolveDiscordAddress(
  client: DiscordClient,
  input: ChannelResolveAddressOperation,
  context: {
    installation: Parameters<CoreClient['publishDefinition']>[0]
    publishDefinition: CoreClient['publishDefinition']
  },
  operation: Omit<OperationRetryOptions, 'idempotent'>,
): Promise<ChannelRegistrationTarget> {
  if (!discordID.safeParse(input.provider_ref).success)
    throw new DiscordAddressError('invalid_address')
  const kind = input.provider_ref_kind
  if (kind !== undefined && kind !== 'channel' && kind !== 'thread')
    throw new DiscordAddressError('unsupported_address')
  const result = await retryOperation({ ...operation, idempotent: true }, async (attempt) => {
    let address
    try {
      address = await loadDiscordAddress(client, input.provider_ref, kind, attempt)
    } catch (error) {
      if (error instanceof DiscordAddressError) return error
      if (error instanceof DiscordAPIError && error.code === 'address_unavailable')
        return new DiscordAddressError('address_unavailable')
      throw error
    }
    // Core configuration/publication failures must not become provider 404s.
    const definition = await context.publishDefinition(
      context.installation,
      discordDefinition(address.kind === 'channel' ? 'channel' : 'thread'),
      attempt.signal,
    )
    const target: ChannelRegistrationTarget = {
      definition_id: definition.id,
      provider_ref: address.channel.id,
      provider_ref_kind: address.kind,
      display_name: address.channel.name,
    }
    if (address.parent) {
      const parent = await context.publishDefinition(
        context.installation,
        discordDefinition('channel'),
        attempt.signal,
      )
      target.parent = {
        definition_id: parent.id,
        provider_ref: address.parent.id,
        provider_ref_kind: 'channel',
        display_name: address.parent.name,
      }
    }
    return target
  })
  if (result instanceof DiscordAddressError) throw result
  return result
}
