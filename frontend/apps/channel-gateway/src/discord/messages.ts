import type {
  ChannelContinuationError,
  ChannelProviderMessageObservation,
  ChannelSendOperation,
} from '@omnara/sdk'

import { type OperationRetryOptions, retryOperation } from '../operations/retry'
import { DiscordAddressError, discordDestination, loadDiscordAddress } from './address'
import type { DiscordClient, DiscordUpload } from './client'
import {
  DiscordAPIError,
  type DiscordChannel,
  type DiscordMessage,
  discordRootParams,
  discordThreadParams,
} from './protocol'

/** Native facts, not the private operation wire. The provider must preserve a
 * known root publication even if its separately requested thread failed.
 */
export interface DiscordPublication {
  message: DiscordMessage
  thread?: DiscordChannel
  continuationError?: ChannelContinuationError
}
interface DiscordSendState {
  verified: boolean
  message?: DiscordMessage
  thread?: DiscordChannel
}

export async function sendDiscordMessage(
  client: DiscordClient,
  input: ChannelSendOperation,
  artifacts: readonly DiscordUpload[],
  operation: Omit<OperationRetryOptions, 'idempotent'>,
): Promise<DiscordPublication> {
  const target = discordDestination(input.destination)
  const parsed = discordRootParams.safeParse(input.params)
  if (
    !parsed.success ||
    (target.kind === 'thread' && !discordThreadParams.safeParse(input.params).success)
  )
    throw new DiscordAPIError('unsupported_params')
  const threadName = parsed.data.thread_name
  const grants = input.reply_channel_grants
  if (
    grants !== undefined &&
    (!(grants.receive || grants.read || grants.send) || target.kind !== 'channel')
  )
    throw new DiscordAPIError('invalid_reply_grants')
  if (
    threadName !== undefined &&
    (!grants || !threadName.trim() || Buffer.from(threadName).toString('utf8') !== threadName)
  )
    throw new DiscordAPIError('invalid_thread_name')
  const ids = input.message.artifact_ids ?? []
  const byID = new Map(artifacts.map((file) => [file.id, file]))
  if (
    new Set(ids).size !== ids.length ||
    byID.size !== artifacts.length ||
    artifacts.length !== ids.length
  )
    throw new DiscordAPIError('artifact_mismatch')
  const files = ids.map((id) => {
    const file = byID.get(id)
    if (!file) throw new DiscordAPIError('artifact_mismatch')
    return file
  })
  client.validateMessage(input.message.text, files)
  const state: DiscordSendState = { verified: false }
  try {
    await retryOperation(operation, async (attempt) => {
      if (!state.verified) {
        try {
          await loadDiscordAddress(client, target.id, target.kind, attempt)
        } catch (error) {
          if (error instanceof DiscordAddressError) throw new DiscordAPIError(error.code)
          throw error
        }
        state.verified = true
      }
      if (!state.message) {
        const published = await client.createMessage(target.id, input.message.text, files, attempt)
        if (
          published.channel_id !== target.id ||
          published.author.id !== client.configuration.botUserID ||
          (published.guild_id !== undefined && published.guild_id !== client.configuration.guildID)
        )
          throw new DiscordAPIError('invalid_response', { outcomeUnknown: true })
        state.message = published
      }
      if (grants && !state.thread) {
        const thread = await client.createThread(
          target.id,
          state.message.id,
          threadName ?? 'Omnara conversation',
          attempt,
        )
        if (
          thread.id !== state.message.id ||
          thread.parent_id !== target.id ||
          thread.guild_id !== client.configuration.guildID ||
          thread.type !== 11
        )
          throw new DiscordAPIError('invalid_response', { outcomeUnknown: true })
        state.thread = thread
      }
    })
  } catch (error) {
    if (!state.message) throw error
    if (grants && !state.thread)
      return {
        message: state.message,
        continuationError: {
          code: 'reply_channel_unavailable',
          message:
            'The Discord message was published, but its reply thread could not be made available.',
        },
      }
  }
  if (!state.message) throw new DiscordAPIError('invalid_response', { outcomeUnknown: true })
  return { message: state.message, thread: state.thread }
}

/** Preserve Discord markdown and native mention IDs without remote enrichment;
 * rich content/files remain explicit partial coverage.
 */
export function discordObservation(message: DiscordMessage, guild: string, parent?: string) {
  let partial =
    !message.content.trim() ||
    message.attachments.length > 0 ||
    message.embeds.length > 0 ||
    Boolean(message.components?.length) ||
    Boolean(message.sticker_items?.length) ||
    message.poll !== undefined
  const bytes = Buffer.from(message.content)
  if (bytes.length > 64 * 1024) partial = true
  const text = new TextDecoder().decode(bytes.subarray(0, 64 * 1024), { stream: true })
  const observation: ChannelProviderMessageObservation = {
    content: text.trim() ? { text } : {},
    publication: 'published',
    message_id: message.id,
    author: {
      ref: message.author.id,
      display_name: message.author.global_name ?? message.author.username,
    },
    created_at: new Date(message.timestamp).toISOString(),
  }
  if (message.attachments.length)
    observation.metadata = {
      discord_attachment_ids: message.attachments.map((file) => file.id),
    }
  const reference = message.message_reference
  if (
    reference?.message_id &&
    (reference.type === undefined || reference.type === 0) &&
    (reference.guild_id === undefined || reference.guild_id === guild)
  ) {
    if (reference.channel_id === undefined || reference.channel_id === message.channel_id)
      observation.reply_to = { message_id: reference.message_id }
    else if (reference.channel_id === parent)
      observation.reply_to = {
        message_id: reference.message_id,
        destination: {
          implementation_key: 'discord_channel',
          provider_ref: parent,
          provider_ref_kind: 'channel',
        },
      }
    else partial = true
  } else if (reference) partial = true
  const thread = message.thread
  if (thread) {
    if (
      thread.type === 11 &&
      thread.id === message.id &&
      thread.guild_id === guild &&
      thread.parent_id === message.channel_id
    ) {
      observation.reply_channel = {
        implementation_key: 'discord_thread',
        provider_ref: thread.id,
        provider_ref_kind: 'thread',
        display_name: thread.name,
      }
    } else partial = true
  }
  return { observation, partial }
}
