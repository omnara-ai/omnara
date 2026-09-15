import { createSlackAdapter, type SlackEvent } from '@chat-adapter/slack'
import { stringifyMarkdown } from 'chat'

import { SlackAPIError } from './client'
import type { SlackMessage } from './protocol'

export interface SlackCredentials {
  botToken: string
  signingSecret: string
  botUserId: string
}

export interface SlackHistoryMessage {
  timestamp: string
  text: string
  authorRef?: string
  files: { id: string; name?: string }[]
  /** True when text alone does not fully represent the native message. */
  partial: boolean
}

/** Public SDK parsing only: no webhook dispatch, installation persistence or I/O.
 * Explicit bot identity avoids auth.test; all provider I/O belongs to SlackClient.
 */
export function createSlackMessageParser(
  credentials: SlackCredentials,
): (raw: SlackMessage) => SlackHistoryMessage {
  if (!credentials.botToken || !credentials.signingSecret || !credentials.botUserId) {
    throw new SlackAPIError('invalid_configuration')
  }
  const adapter = createSlackAdapter({
    botToken: credentials.botToken,
    signingSecret: credentials.signingSecret,
    botUserId: credentials.botUserId,
    webClientOptions: { retryConfig: { retries: 0 }, rejectRateLimitedCalls: true },
  })
  return (raw) => {
    // Pass only validated scalar fields into the SDK. Files are described below;
    // do not expose its lazy fetchData callbacks that lack our operation signal.
    const event: SlackEvent = { type: 'message', ts: raw.ts, text: raw.text }
    for (const key of ['channel', 'thread_ts', 'user', 'bot_id', 'username'] as const) {
      if (raw[key] !== undefined) event[key] = raw[key]
    }
    const files: SlackHistoryMessage['files'] = []
    if (raw.files !== undefined) {
      for (const file of raw.files) {
        files.push({ id: file.id, name: file.name })
      }
    }
    const parsed = adapter.parseMessage(event)
    return {
      timestamp: raw.ts,
      text: stringifyMarkdown(parsed.formatted),
      authorRef: event.user ?? event.bot_id,
      files,
      partial:
        files.length > 0 ||
        (raw.blocks?.length ?? 0) > 0 ||
        (raw.attachments?.length ?? 0) > 0 ||
        raw.subtype === 'message_deleted',
    }
  }
}
