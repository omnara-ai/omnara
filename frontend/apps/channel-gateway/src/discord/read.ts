import type { ChannelReadOperation, ChannelReadOperationResult } from '@omnara/sdk'

import { parseObjectFields } from '../json'
import { type OperationRetryOptions, retryOperation } from '../operations/retry'
import { discordDestination, loadDiscordAddress } from './address'
import type { DiscordClient } from './client'
import { discordObservation } from './messages'
import { DiscordAPIError, discordCursor } from './protocol'

/** Native newest-first pagination by exact snowflake. Reading does not create
 * threads or register reply addresses; core maps existing authorized children.
 */
export async function readDiscordOperation(
  client: DiscordClient,
  input: ChannelReadOperation,
  operation: Omit<OperationRetryOptions, 'idempotent'>,
): Promise<ChannelReadOperationResult> {
  const target = discordDestination(input.destination)
  if (!Number.isInteger(input.limit) || input.limit < 1 || input.limit > 100)
    throw new DiscordAPIError('invalid_history_limit')
  const before = decodeCursor(input.cursor, client.configuration.guildID, target.id)
  return retryOperation({ ...operation, idempotent: true }, async (attempt) => {
    const address = await loadDiscordAddress(client, target.id, target.kind, attempt)
    const page = await client.getMessages(target.id, input.limit, before, attempt)
    const messages = []
    // Discord also returns [] when READ_MESSAGE_HISTORY is missing. Do not claim
    // independently verified completeness for that indistinguishable response.
    let partial = page.length === 0
    let previous = before
    for (const message of page) {
      if (
        message.channel_id !== target.id ||
        (message.guild_id !== undefined && message.guild_id !== client.configuration.guildID) ||
        (previous !== undefined && BigInt(message.id) >= BigInt(previous))
      )
        throw new DiscordAPIError('invalid_history_response')
      previous = message.id
      const result = discordObservation(message, client.configuration.guildID, address.parent?.id)
      partial ||= result.partial
      messages.push(result.observation)
    }
    const result: ChannelReadOperationResult = {
      messages,
      coverage: partial ? 'partial' : 'complete',
    }
    if (partial)
      result.coverage_reason =
        'Discord returned empty or incomplete content. History permission, message-content access, files and rich content may limit representation.'
    if (page.length === input.limit && previous !== undefined)
      result.next_cursor = Buffer.from(
        JSON.stringify({
          guild: client.configuration.guildID,
          channel: target.id,
          before: previous,
        }),
      ).toString('base64url')
    return result
  })
}

function decodeCursor(
  cursor: string | undefined,
  guild: string,
  channel: string,
): string | undefined {
  if (cursor === undefined) return undefined
  try {
    if (!cursor || cursor.length > 4096 || !/^[A-Za-z0-9_-]+$/.test(cursor))
      throw new Error('cursor')
    const bytes = Buffer.from(cursor, 'base64url')
    if (bytes.toString('base64url') !== cursor) throw new Error('cursor')
    const raw = new TextDecoder('utf-8', { fatal: true }).decode(bytes)
    parseObjectFields(raw, 4096)
    const parsed = discordCursor.safeParse(JSON.parse(raw))
    if (!parsed.success || parsed.data.guild !== guild || parsed.data.channel !== channel)
      throw new Error('cursor')
    return parsed.data.before
  } catch {
    throw new DiscordAPIError('invalid_history_cursor')
  }
}
