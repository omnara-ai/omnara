import type { ChannelConnectorInstallationConfiguration } from '@omnara/sdk'

import type { OperationAttemptContext } from '../operations/retry'
import { SlackAPIError, type SlackClient } from './client'
import {
  referencedChannels,
  referencedUsers,
  renderSlackText,
  type SlackInboundEvent,
  type SlackLabels,
  userReference,
} from './events'
import type { SlackMessage } from './protocol'

export interface SlackEnrichment {
  labels: SlackLabels
  history: string
  historyStatus: 'skipped' | 'fetched' | 'empty' | 'rate_limited' | 'failed'
}

/** Native optional enrichment: one 1.5s budget for a 15-message context page and
 * at most eight user/two channel labels. Failure cannot discard the actual input.
 */
export async function enrichSlackInput(
  client: SlackClient,
  event: SlackInboundEvent,
  fetchHistory: boolean,
  install: ChannelConnectorInstallationConfiguration,
  botUserID: string,
  context: OperationAttemptContext,
): Promise<SlackEnrichment> {
  const controller = new AbortController()
  const deadlineMs = Math.min(context.deadlineMs, Date.now() + 1500)
  const timer = setTimeout(
    () => {
      controller.abort()
    },
    Math.max(0, deadlineMs - Date.now()),
  )
  const attempt = {
    ...context,
    deadlineMs,
    signal: AbortSignal.any([context.signal, controller.signal]),
  }
  const users = new Map<string, string>()
  const channels = new Map<string, string>()
  if (install.install.display_name.trim()) users.set(botUserID, install.install.display_name.trim())
  let messages: SlackMessage[] = []
  let historyStatus: SlackEnrichment['historyStatus'] = 'skipped'
  try {
    if (fetchHistory) {
      try {
        const args = { channel: event.channel, latest: event.ts, inclusive: false, limit: 15 }
        const page =
          event.thread_ts && event.thread_ts !== event.ts
            ? await client.api('conversations.replies', { ...args, ts: event.thread_ts }, attempt)
            : await client.api('conversations.history', args, attempt)
        messages = page.messages
          .filter((message) => message.ts !== event.ts && message.text.trim())
          .sort((a, b) => compareTimestamp(a.ts, b.ts))
        historyStatus = messages.length ? 'fetched' : 'empty'
      } catch (error) {
        historyStatus =
          error instanceof SlackAPIError && error.code === 'ratelimited' ? 'rate_limited' : 'failed'
      }
    }
    const userIDs = new Set([
      event.user,
      ...referencedUsers(event.text),
      ...messages.flatMap((message) => [message.user ?? '', ...referencedUsers(message.text)]),
    ])
    const channelIDs = new Set([
      event.channel,
      ...referencedChannels(event.text),
      ...messages.flatMap((message) => referencedChannels(message.text)),
    ])
    // All independent label lookups share the remaining budget. Rejections are
    // consumed and yield the stable provider IDs already present in the input.
    await Promise.allSettled([
      ...Array.from(userIDs)
        .filter((id) => id && !users.has(id))
        .slice(0, 8)
        .map(async (id) => {
          const { user } = await client.api('users.info', { user: id }, attempt)
          const name = [
            user.profile?.display_name,
            user.profile?.real_name,
            user.profile?.name,
            user.real_name,
            user.name,
          ]
            .find((value) => value?.trim())
            ?.trim()
          if (name) users.set(id, name)
        }),
      ...Array.from(channelIDs)
        .filter(Boolean)
        .slice(0, 2)
        .map(async (id) => {
          const { channel } = await client.api('conversations.info', { channel: id }, attempt)
          if (channel.name?.trim()) channels.set(id, channel.name.trim())
        }),
    ])
    context.signal.throwIfAborted()
    const labels = { users, channels }
    const lines = messages.map(
      (message) =>
        `${userReference(message.user ?? '', labels)}: ${renderSlackText(message.text.trim(), labels, true)}`,
    )
    return {
      labels,
      history: lines.length ? `Recent Slack context:\n${lines.join('\n')}` : '',
      historyStatus,
    }
  } finally {
    clearTimeout(timer)
    controller.abort()
  }
}

function compareTimestamp(a: string, b: string): number {
  const micros = (value: string): bigint => {
    const [seconds = '0', fraction = ''] = value.split('.')
    return BigInt(seconds) * 1_000_000n + BigInt(fraction.padEnd(6, '0'))
  }
  return micros(a) < micros(b) ? -1 : micros(a) > micros(b) ? 1 : 0
}
