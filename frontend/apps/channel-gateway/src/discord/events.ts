import type { ChannelConnectorEventReceipt, CreateAgentInputContentBlock } from '@omnara/sdk'
import { z } from 'zod'

import { DiscordAPIError, discordID, discordMessage } from './protocol'

export const discordAttachment = z.object({
  id: discordID,
  filename: z.string().min(1).max(1024),
  size: z.number().int().nonnegative(),
  content_type: z.string().max(256).optional(),
  url: z.string().max(4096),
})
const discordInboundMessage = discordMessage.extend({
  mentions: z.array(z.object({ id: discordID })).max(100),
  attachments: z.array(discordAttachment).max(100),
  webhook_id: discordID.optional(),
})
export type DiscordInboundMessage = z.infer<typeof discordInboundMessage>
export type DiscordAttachment = z.infer<typeof discordAttachment>

/** The runtime already saved this raw Dispatch under the exact receipt lease.
 * Replay processes the saved payload without re-entering live capture.
 */
export function parseDiscordReceipt(receipt: Readonly<ChannelConnectorEventReceipt>) {
  const envelope = z
    .object({ op: z.literal(0), t: z.string(), s: z.number().int().nonnegative(), d: z.unknown() })
    .safeParse(receipt.payload)
  if (
    !envelope.success ||
    !receipt.event_id.startsWith('discord:') ||
    !receipt.event_id.endsWith(`:${envelope.data.s}`)
  )
    throw new DiscordAPIError('invalid_event')
  if (envelope.data.t !== 'MESSAGE_CREATE') return undefined
  const probe = z
    .object({ type: z.number().int(), guild_id: discordID.optional() })
    .safeParse(envelope.data.d)
  if (!probe.success) throw new DiscordAPIError('invalid_event')
  // Guild text/public-thread ordinary messages and replies. No DM, system,
  // thread-starter copies, crossposts or interaction-generated messages.
  if (!probe.data.guild_id || ![0, 19].includes(probe.data.type)) return undefined
  const parsed = discordInboundMessage.safeParse(envelope.data.d)
  if (!parsed.success) throw new DiscordAPIError('invalid_event')
  if (parsed.data.author.bot || parsed.data.webhook_id) return undefined
  return parsed.data
}

export function discordInputKey(event: DiscordInboundMessage): string {
  return `discord:message:${event.guild_id}:${event.channel_id}:${event.id}`
}

export function discordInputText(
  event: DiscordInboundMessage,
  providerRef: string,
): CreateAgentInputContentBlock[] {
  const rich = [
    event.embeds.length,
    event.components?.length,
    event.sticker_items?.length,
    event.poll,
  ].some(Boolean)
  if (!event.content.trim() && !event.attachments.length && !rich)
    // A working mention is not proof of Message Content intent. The runtime
    // checks the actual app flag; an empty ordinary payload cannot stand in for
    // inaccessible content and silently become a successful input here.
    throw new DiscordAPIError('message_content_unavailable')
  const blocks: CreateAgentInputContentBlock[] = [
    {
      type: 'text',
      text: `Discord user ${event.author.id} in guild ${event.guild_id}, channel ${event.channel_id}, thread ${providerRef}, message ${event.id}:\n`,
      metadata: { omnara_hidden: 'true' },
    },
  ]
  if (event.content) blocks.push({ type: 'text', text: event.content })
  if (rich)
    blocks.push({
      type: 'text',
      text: '[Discord embeds, stickers, components or poll are not included.]',
    })
  return blocks
}
