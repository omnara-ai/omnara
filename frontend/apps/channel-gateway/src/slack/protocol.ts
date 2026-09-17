import type { WebClient } from '@slack/web-api'
import { z } from 'zod'

export const slackTimestamp = z.string().regex(/^\d{1,20}\.\d{1,6}$/)

// Slack response parsing happens once, at the HTTP boundary. Unknown provider
// fields are stripped; none become public metadata or implicit authority.
export const slackEnvelope = z.object({ ok: z.boolean(), error: z.string().optional() })
const share = z.object({ ts: slackTimestamp, thread_ts: slackTimestamp.optional() })
export const slackEventFile = z.object({
  id: z.string().default(''),
  name: z.string().default(''),
  title: z.string().default(''),
  mimetype: z.string().default(''),
  size: z.number().int().nonnegative().default(0),
  url_private: z.string().default(''),
  url_private_download: z.string().default(''),
  file_access: z.string().default(''),
})
export type SlackEventFile = z.infer<typeof slackEventFile>
const file = z.object({
  id: z.string().min(1).max(512),
  name: z.string().max(4096).optional(),
  shares: z
    .object({
      public: z.record(z.string(), z.array(share)).optional(),
      private: z.record(z.string(), z.array(share)).optional(),
    })
    .optional(),
})
export const slackMessage = z.object({
  ts: slackTimestamp,
  text: z.string().default(''),
  channel: z.string().optional(),
  thread_ts: slackTimestamp.optional(),
  user: z.string().max(512).optional(),
  bot_id: z.string().max(512).optional(),
  username: z.string().optional(),
  subtype: z.string().optional(),
  files: z.array(file).optional(),
  blocks: z.array(z.unknown()).optional(),
  attachments: z.array(z.unknown()).optional(),
})
export type SlackMessage = z.infer<typeof slackMessage>
const slackHistory = z.object({
  messages: z.array(slackMessage).max(100),
  has_more: z.boolean().optional(),
  is_limited: z.boolean().optional(),
  response_metadata: z.object({ next_cursor: z.string().max(8192).optional() }).optional(),
})
export type SlackHistory = z.infer<typeof slackHistory>

export const slackResponses = {
  'reactions.add': z.object({}),
  'chat.update': z.object({ ts: slackTimestamp, channel: z.string().optional() }),
  'chat.postMessage': z.object({
    ts: slackTimestamp,
    channel: z.string().optional(),
    response_metadata: z.object({ warnings: z.array(z.string()).optional() }).optional(),
  }),
  'files.getUploadURLExternal': z.object({
    upload_url: z.string().min(1),
    file_id: z.string().min(1).max(512),
  }),
  'files.completeUploadExternal': z.object({ files: z.array(file).optional() }),
  'conversations.history': slackHistory,
  'conversations.replies': slackHistory,
  'files.info': z.object({ file: slackEventFile }),
  'users.info': z.object({
    user: z.object({
      name: z.string().optional(),
      real_name: z.string().optional(),
      profile: z
        .object({
          display_name: z.string().optional(),
          real_name: z.string().optional(),
          name: z.string().optional(),
        })
        .nullish(),
    }),
  }),
  'conversations.info': z.object({
    channel: z.object({
      id: z.string().optional(),
      name: z.string().optional(),
      is_channel: z.boolean().optional(),
      is_group: z.boolean().optional(),
      is_im: z.boolean().optional(),
      is_mpim: z.boolean().optional(),
      context_team_id: z.string().optional(),
    }),
  }),
}
export type SlackMethod = keyof typeof slackResponses
export type SlackResponse<M extends SlackMethod> = z.infer<(typeof slackResponses)[M]>
export interface SlackRequest {
  'reactions.add': Parameters<WebClient['reactions']['add']>[0]
  'chat.update': Parameters<WebClient['chat']['update']>[0]
  'chat.postMessage': Parameters<WebClient['chat']['postMessage']>[0]
  'files.getUploadURLExternal': Parameters<WebClient['files']['getUploadURLExternal']>[0]
  'files.completeUploadExternal': Parameters<WebClient['files']['completeUploadExternal']>[0]
  'conversations.history': Parameters<WebClient['conversations']['history']>[0]
  'conversations.replies': Parameters<WebClient['conversations']['replies']>[0]
  'files.info': Parameters<WebClient['files']['info']>[0]
  'users.info': Parameters<WebClient['users']['info']>[0]
  'conversations.info': Parameters<WebClient['conversations']['info']>[0]
}

export const slackHistoryCursor = z.strictObject({
  channel: z.string(),
  threadTs: slackTimestamp.nullable(),
  before: slackTimestamp,
})
