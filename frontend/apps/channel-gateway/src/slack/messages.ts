import { renderSlackText } from './events'
import type { SlackMessage } from './protocol'

export interface SlackHistoryMessage {
  timestamp: string
  text: string
  authorRef?: string
  files: { id: string; name?: string }[]
  /** True when text alone does not fully represent the native message. */
  partial: boolean
}

/** Preserve the same provider markup and stable IDs as admitted Slack input.
 * Markdown AST round trips can alter code and discard mention identities.
 */
export function parseSlackMessage(raw: SlackMessage): SlackHistoryMessage {
  const files = (raw.files ?? []).map((file) => ({ id: file.id, name: file.name }))
  return {
    timestamp: raw.ts,
    text: renderSlackText(raw.text, { users: new Map(), channels: new Map() }, true),
    authorRef: raw.user ?? raw.bot_id,
    files,
    partial:
      files.length > 0 ||
      (raw.blocks?.length ?? 0) > 0 ||
      (raw.attachments?.length ?? 0) > 0 ||
      raw.subtype === 'message_deleted',
  }
}
