import {
  type ChannelOperationDestination,
  type ChannelReadOperation,
  type ChannelReadOperationResult,
  type ChannelSendOperation,
  type ChannelSendOperationResult,
  schemas,
} from '@omnara/sdk'

import type { OperationArtifact } from '../operations/files'
import type { OperationRetryOptions } from '../operations/retry'
import { SlackAPIError, type SlackClient, type SlackUpload } from './client'
import type { SlackHistoryMessage } from './messages'
import type { SlackMessage } from './protocol'
import { readSlackHistory } from './read'
import { sendSlackMessage, type SlackDestination, validateDestination } from './send'

/** The caller dispatches the authorized implementation key and parses the generated
 * operation schema. This layer maps only Slack addresses and provider facts.
 * threadImplementationKey comes from the registered Slack definitions, not params.
 */
export async function sendSlackOperation(
  client: SlackClient,
  input: ChannelSendOperation,
  artifacts: readonly (SlackUpload & Pick<OperationArtifact, 'id'>)[],
  threadImplementationKey: string,
  operation: Omit<OperationRetryOptions, 'idempotent'>,
): Promise<ChannelSendOperationResult> {
  const target = slackDestination(input.destination)
  if (Object.keys(input.params).length !== 0) throw new SlackAPIError('unsupported_params')
  const ids = input.message.artifact_ids ?? []
  const byID = new Map(artifacts.map((artifact) => [artifact.id, artifact]))
  if (
    new Set(ids).size !== ids.length ||
    byID.size !== artifacts.length ||
    artifacts.length !== ids.length
  )
    throw new SlackAPIError('artifact_mismatch')
  const files = ids.map((id) => {
    const file = byID.get(id)
    if (!file) throw new SlackAPIError('artifact_mismatch')
    return file
  })
  const mayOpenThread =
    input.destination.provider_ref_kind === 'channel' && input.reply_channel_grants !== undefined
  if (
    mayOpenThread &&
    !schemas.zChannelReplyDestination.shape.implementation_key.safeParse(threadImplementationKey)
      .success
  ) {
    throw new SlackAPIError('invalid_configuration')
  }
  const published = await sendSlackMessage(
    client,
    {
      ...target,
      text: input.message.text,
      artifacts: files,
    },
    operation,
  )
  return {
    publication: published.publication,
    // A root post is contained in the channel even when replies form a child.
    message_channel: 'destination',
    message_id: published.messageTs,
    metadata: published.fileIds.length ? { slack_file_ids: published.fileIds } : undefined,
    reply_channel:
      mayOpenThread && published.messageTs
        ? {
            implementation_key: threadImplementationKey,
            provider_ref: `${target.channel}:${published.messageTs}`,
            provider_ref_kind: 'thread',
          }
        : undefined,
  }
}

/** Core supplies the addressed channel and reauthorizes artifact references.
 * Until file import is connected, preserve native IDs as metadata and report
 * partial representation; never turn them into Omnara artifact IDs.
 */
export async function readSlackOperation(
  client: SlackClient,
  parseMessage: (raw: SlackMessage) => SlackHistoryMessage,
  input: ChannelReadOperation,
  operation: Omit<OperationRetryOptions, 'idempotent'>,
): Promise<ChannelReadOperationResult> {
  const page = await readSlackHistory(
    client,
    parseMessage,
    {
      ...slackDestination(input.destination),
      limit: input.limit,
      cursor: input.cursor,
    },
    operation,
  )
  let partial = page.coverage === 'partial'
  const messages = page.messages.map((message) => {
    const bytes = Buffer.from(message.text)
    if (bytes.length > 64 * 1024 || !message.text.trim()) partial = true
    // Streaming decode withholds a final incomplete code point at the byte bound.
    const text = new TextDecoder('utf-8').decode(bytes.subarray(0, 64 * 1024), { stream: true })
    const observation: ChannelReadOperationResult['messages'][number] = {
      content: { text: text.trim() ? text : undefined },
      publication: 'published',
      message_id: message.timestamp,
      author: message.authorRef ? { ref: message.authorRef } : undefined,
      metadata: message.files.length
        ? { slack_file_ids: message.files.map((file) => file.id) }
        : undefined,
    }
    return observation
  })
  return {
    messages,
    next_cursor: page.nextCursor,
    coverage: partial ? 'partial' : 'complete',
    coverage_reason: partial
      ? 'Slack history or message content is incomplete. Text may be bounded; files and rich content are not fully imported.'
      : undefined,
  }
}

export function slackDestination(input: ChannelOperationDestination): SlackDestination {
  let target: SlackDestination
  switch (input.provider_ref_kind) {
    case 'channel':
    case 'dm':
      target = { channel: input.provider_ref }
      break
    case 'thread': {
      const [channel, threadTs, extra] = input.provider_ref.split(':')
      if (!channel || !threadTs || extra !== undefined)
        throw new SlackAPIError('invalid_destination')
      target = { channel, threadTs }
      break
    }
    default:
      throw new SlackAPIError('unsupported_destination')
  }
  validateDestination(target)
  return target
}
