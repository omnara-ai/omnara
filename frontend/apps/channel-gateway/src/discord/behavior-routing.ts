import {
  type ChannelConnectorEventReceipt,
  type ChannelConnectorRecipient,
  type LookupChannelConnectorWorkflowResponse,
  schemas,
} from '@omnara/sdk'
import { z } from 'zod'

import { ReceiptClientError } from '../core/receipt-http'
import type { OperationAttemptContext } from '../operations/retry'
import { loadDiscordAddress } from './address'
import type { DiscordBehaviorContext } from './behavior'
import type { DiscordClient } from './client'
import type { DiscordInboundMessage } from './events'
import { DiscordAPIError } from './protocol'

const routeSchema = schemas.zChannelConnectorRoute
  .extend({
    behavior_key: z.literal('discord_conversation'),
    configuration: z.strictObject({}),
  })
  .strict()
export type DiscordInputTarget =
  | { kind: 'recipient'; recipient: ChannelConnectorRecipient }
  | {
      kind: 'workflow'
      routeID: string
      lookup: LookupChannelConnectorWorkflowResponse
      channelID?: string
      parentChannelID?: string
    }

/** Listener history, not read/send authority, decides whether launching is allowed.
 * Partial workflow fanout continues using core's receipt-scoped outcome fact.
 */
export async function* discordInputTargets(
  receipt: Readonly<ChannelConnectorEventReceipt>,
  context: DiscordBehaviorContext,
  providerRef: string,
  inputKey: string,
  mentioned: boolean,
  signal: AbortSignal,
): AsyncGenerator<DiscordInputTarget> {
  const core = context.core
  const query = { provider_ref: providerRef, input_keys: [inputKey] }
  let page = await core.lookupRecipients(receipt, query, signal)
  if (page.has_receive_binding_history && !page.workflow_started) {
    const cursors = new Set<string>()
    for (;;) {
      for (const recipient of page.recipients) yield { kind: 'recipient', recipient }
      if (page.next_cursor === null) return
      if (cursors.has(page.next_cursor)) throw new ReceiptClientError('invalid_response')
      cursors.add(page.next_cursor)
      page = await core.lookupRecipients(receipt, { ...query, cursor: page.next_cursor }, signal)
    }
  }
  if (!mentioned) return
  const work = context.reserveWorkBytes(0)
  let routes: string[]
  try {
    const raw = await core.listRoutes(receipt, signal, work)
    const parsed = z.array(routeSchema).max(64).safeParse(raw)
    if (!parsed.success) throw new DiscordAPIError('unsupported_route_configuration')
    routes = parsed.data.map((route) => route.id)
  } finally {
    work.release()
  }
  for (const routeID of routes) {
    const lookup = await core.lookupWorkflow(
      receipt,
      {
        route_id: routeID,
        instance_key: providerRef,
        input_keys: [inputKey],
      },
      signal,
    )
    if (lookup.agent_state !== 'archived')
      yield {
        kind: 'workflow',
        routeID,
        lookup,
        channelID: page.channel_id,
        parentChannelID: page.parent_channel_id,
      }
  }
}

/** A message can own exactly one native public thread, with the message's ID.
 * Read before creation, including every receipt replay; a lost POST response is
 * recovered through that deterministic native address, never by posting again.
 */
export async function ensureDiscordMentionThread(
  client: DiscordClient,
  event: DiscordInboundMessage,
  attempt: OperationAttemptContext,
) {
  const existing = async () => {
    const address = await loadDiscordAddress(client, event.id, 'thread', attempt)
    if (address.channel.parent_id !== event.channel_id)
      throw new DiscordAPIError('thread_parent_mismatch')
    return address.channel
  }
  try {
    return await existing()
  } catch (error) {
    if (!(error instanceof DiscordAPIError) || error.code !== 'address_unavailable') throw error
  }
  try {
    const thread = await client.createThread(
      event.channel_id,
      event.id,
      'Omnara conversation',
      attempt,
    )
    if (
      thread.id !== event.id ||
      thread.parent_id !== event.channel_id ||
      thread.guild_id !== client.configuration.guildID ||
      thread.type !== 11
    )
      throw new DiscordAPIError('invalid_response', { outcomeUnknown: true })
    return thread
  } catch (error) {
    attempt.signal.throwIfAborted()
    // Concurrent duplicate receipt or accepted POST with a lost response.
    try {
      return await existing()
    } catch {
      throw error
    }
  }
}
