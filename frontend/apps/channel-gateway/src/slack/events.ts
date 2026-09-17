import type { ChannelConnectorEventReceipt, CreateAgentInputContentBlock } from '@omnara/sdk'
import { z } from 'zod'

import { SlackAPIError } from './errors'
import { slackEventFile, slackTimestamp } from './protocol'

const envelopeSchema = z.object({
  type: z.string(),
  team_id: z.string(),
  api_app_id: z.string(),
  event_id: z.string(),
  authorizations: z
    .array(z.object({ team_id: z.string(), user_id: z.string(), is_bot: z.boolean() }))
    .default([]),
  event: z.unknown(),
})
const probe = z.object({ type: z.string(), subtype: z.string().optional() })
const eventSchema = z.object({
  type: z.enum(['app_mention', 'message']),
  subtype: z.string().default(''),
  user: z.string().default(''),
  bot_id: z.string().default(''),
  text: z.string().default(''),
  channel: z.string(),
  channel_type: z.string().default(''),
  ts: slackTimestamp,
  thread_ts: slackTimestamp.optional(),
  team: z.string().default(''),
  source_team: z.string().default(''),
  user_team: z.string().default(''),
  files: z.array(slackEventFile).default([]),
})
export type SlackInboundEvent = z.infer<typeof eventSchema>
export interface SlackInboundRoute {
  providerRef: string
  kind: 'dm' | 'thread'
  appendOnly: boolean
}
export interface SlackLabels {
  users: ReadonlyMap<string, string>
  channels: ReadonlyMap<string, string>
}

/** Receipts already passed signature verification and strict JSON ingress in core.
 * Parsing saved data never re-enters a live SDK webhook or TTL dedupe path.
 */
export function parseSlackReceipt(receipt: Readonly<ChannelConnectorEventReceipt>) {
  if (Buffer.byteLength(JSON.stringify(receipt.payload)) > 1024 * 1024)
    throw new SlackAPIError('event_too_large')
  const envelope = envelopeSchema.safeParse(receipt.payload)
  if (!envelope.success || envelope.data.event_id !== receipt.event_id)
    throw new SlackAPIError('invalid_event')
  if (envelope.data.type !== 'event_callback') return undefined
  const eventType = probe.safeParse(envelope.data.event)
  if (!eventType.success || !['app_mention', 'message'].includes(eventType.data.type))
    return undefined
  if (eventType.data.subtype && eventType.data.subtype !== 'file_share') return undefined
  const event = eventSchema.safeParse(envelope.data.event)
  if (!event.success) throw new SlackAPIError('invalid_event')
  return { ...envelope.data, event: event.data }
}

export function slackInboundRoute(
  event: SlackInboundEvent,
  botUserID: string,
): SlackInboundRoute | undefined {
  const mentioned = referencedUsers(event.text).includes(botUserID)
  const existingThread = event.thread_ts !== undefined && event.thread_ts !== event.ts
  const thread = event.thread_ts ?? event.ts
  if (event.type === 'app_mention')
    return { providerRef: `${event.channel}:${thread}`, kind: 'thread', appendOnly: false }
  if (event.channel_type === 'im')
    return { providerRef: event.channel, kind: 'dm', appendOnly: false }
  if (!['channel', 'group', 'mpim'].includes(event.channel_type)) return undefined
  if (event.subtype === 'file_share' && event.files.length)
    return { providerRef: `${event.channel}:${thread}`, kind: 'thread', appendOnly: !mentioned }
  // Mentions arrive through app_mention; their ordinary-message sibling cannot launch twice.
  if (!existingThread || mentioned) return undefined
  return { providerRef: `${event.channel}:${thread}`, kind: 'thread', appendOnly: true }
}

export function slackInputKeys(teamID: string, event: SlackInboundEvent) {
  const identity = `${teamID}:${event.channel}:${event.ts}`
  const plain = `slack:message:${identity}`
  const files = `slack:message-files:${identity}`
  if (Buffer.byteLength(files) > 512) throw new SlackAPIError('event_identity_too_large')
  return event.files.length ? { own: files, sibling: plain } : { own: plain, sibling: files }
}

/** Native events.go formatting preserves hidden context, visible labels and IDs.
 * SDK Markdown parsing would change these established input representations.
 */
export function slackInputText(
  event: SlackInboundEvent,
  route: SlackInboundRoute,
  newlyMapped: boolean,
  history: string,
  labels: SlackLabels,
  filesOnly: boolean,
): CreateAgentInputContentBlock[] {
  let location = event.channel_type === 'im' ? 'Slack DM' : channelReference(event.channel, labels)
  if (route.kind === 'thread') location += `, thread ${route.providerRef.split(':')[1]}`
  const prefix = `${userReference(event.user, labels)} in ${location}:\n`
  let hidden = prefix
  if (event.channel_type !== 'im') {
    hidden = inputContext(event, route, newlyMapped)
    if (history) hidden += `\n\n${history}`
    hidden += `\n\n${prefix}`
  }
  const text = filesOnly ? 'Files for the previous Slack message.' : event.text.trim()
  const message = renderSlackText(text, labels, true)
  const blocks: CreateAgentInputContentBlock[] = [
    { type: 'text', text: hidden, metadata: { omnara_hidden: 'true' } },
  ]
  if (message)
    blocks.push({
      type: 'text',
      text: message,
      metadata: filesOnly
        ? { omnara_hidden: 'true' }
        : displayMetadata(message, renderSlackText(text, labels, false)),
    })
  return blocks
}

export function displayMetadata(text: string, display: string) {
  return text === display || Array.from(display).length > 512
    ? undefined
    : { omnara_display_text: display }
}

export function slackChannelDisplayName(
  event: SlackInboundEvent,
  route: SlackInboundRoute,
  labels: SlackLabels,
): string | undefined {
  const user = labels.users.get(event.user)?.trim() ?? ''
  const channel = labels.channels.get(event.channel)?.trim() ?? ''
  const name = route.kind === 'dm' ? (user ? `Slack DM with ${user}` : '') : channel
  // An omitted name preserves prior enrichment on replay or lookup failure.
  // Channel names also match the core's handling of Slack rename events.
  if (!name) return undefined
  // Provider labels are optional enrichment, never permission to exceed the
  // canonical display-name bound (512 UTF-8 bytes).
  return Array.from(name).slice(0, 128).join('')
}

function inputContext(
  event: SlackInboundEvent,
  route: SlackInboundRoute,
  newlyMapped: boolean,
): string {
  if (route.appendOnly)
    return 'This Slack thread may include multiple participants, and not every message is necessarily directed at you. Use your judgment to decide whether to call `send_channel_message` at all.'
  if (event.type === 'app_mention' && event.thread_ts && event.thread_ts !== event.ts)
    return newlyMapped
      ? 'This message directly mentioned the agent inside an existing Slack thread.'
      : 'This message directly mentioned the agent inside a Slack thread that is already attached to this agent.'
  if (event.type === 'app_mention')
    return newlyMapped
      ? 'The agent was mentioned in a Slack channel, so this message starts a new Slack thread for communicating with the agent.'
      : 'This message directly mentioned the agent in a Slack thread that is already attached to this agent.'
  return route.kind === 'thread'
    ? 'This message was routed to a Slack thread attached to this agent.'
    : 'This message was received from Slack.'
}

export function referencedUsers(text: string): string[] {
  return Array.from(text.matchAll(/<@([^>|]+)(?:\|[^>]+)?>/g), (match) => match[1] ?? '')
}
export function referencedChannels(text: string): string[] {
  return Array.from(text.matchAll(/<#([^>|]+)(?:\|[^>]+)?>/g), (match) => match[1] ?? '')
}
export function userReference(id: string, labels: SlackLabels): string {
  if (!id) return 'Slack user'
  const label = labels.users.get(id)?.trim()
  return label && label !== `<@${id}>` ? `<@${id}> (${label.replace(/^@/, '')})` : `<@${id}>`
}
function channelReference(id: string, labels: SlackLabels): string {
  const label = labels.channels.get(id)?.trim()
  return label ? `<#${id}> (#${label.replace(/^#/, '')})` : `<#${id}>`
}
export function renderSlackText(text: string, labels: SlackLabels, includeIDs: boolean): string {
  return text
    .replace(/<@([^>|]+)(?:\|([^>]+))?>/g, (raw: string, id: string, fallback?: string) => {
      const label = [labels.users.get(id), fallback]
        .map((value) => value?.trim())
        .find((value) => value !== undefined && value !== '')
      if (!label || label === `<@${id}>`) return raw
      return includeIDs ? `<@${id}> (${label.replace(/^@/, '')})` : `@${label.replace(/^@/, '')}`
    })
    .replace(/<#([^>|]+)(?:\|([^>]+))?>/g, (raw: string, id: string, fallback?: string) => {
      const label = [labels.channels.get(id), fallback]
        .map((value) => value?.trim())
        .find((value) => value !== undefined && value !== '')
      if (!label) return raw
      return includeIDs ? `<#${id}> (#${label.replace(/^#/, '')})` : `#${label.replace(/^#/, '')}`
    })
    .replace(/<!(here|channel|everyone)(?:\|[^>]*)?>/g, '@$1')
    .replace(/&(amp|lt|gt);/g, (_raw, entity: string) =>
      entity === 'amp' ? '&' : entity === 'lt' ? '<' : '>',
    )
}
