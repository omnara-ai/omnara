import type { JsonBody } from '@omnara/sdk'
import { z } from 'zod'

import { ProviderDeliveryError } from '../types'

/** Snowflakes are unsigned 64-bit decimal strings, never JavaScript numbers. */
export const discordID = z
  .string()
  .refine(
    (value) => /^[1-9][0-9]{0,19}$/.test(value) && BigInt(value) <= 18_446_744_073_709_551_615n,
  )

export class DiscordAPIError extends ProviderDeliveryError {
  constructor(
    readonly code: string,
    options: { retryable?: boolean; outcomeUnknown?: boolean; retryAfterMs?: number } = {},
  ) {
    super(`Discord operation failed: ${code}`, options)
    this.name = 'DiscordAPIError'
  }
}

export const discordChannel = z.object({
  id: discordID,
  type: z.number().int().nonnegative(),
  guild_id: discordID.optional(),
  parent_id: discordID.nullish(),
  name: z.string().min(1).max(100).optional(),
})
export type DiscordChannel = z.infer<typeof discordChannel>

export const discordUser = z.object({
  id: discordID,
  username: z.string().min(1).max(128),
  global_name: z.string().max(128).nullish(),
  bot: z.boolean().optional(),
})
const reference = z.object({
  type: z.number().int().optional(),
  message_id: discordID.optional(),
  channel_id: discordID.optional(),
  guild_id: discordID.optional(),
})
export const discordMessage = z.object({
  id: discordID,
  channel_id: discordID,
  guild_id: discordID.optional(),
  author: discordUser,
  content: z.string().max(64 * 1024),
  timestamp: z.iso.datetime({ offset: true }),
  type: z.number().int(),
  attachments: z.array(z.object({ id: discordID })).max(100),
  embeds: z.array(z.unknown()).max(100),
  components: z.array(z.unknown()).max(100).optional(),
  sticker_items: z.array(z.unknown()).max(100).optional(),
  poll: z.unknown().optional(),
  message_reference: reference.optional(),
  thread: discordChannel.optional(),
})
export type DiscordMessage = z.infer<typeof discordMessage>

export const discordRootParams = z.strictObject({
  thread_name: z.string().min(1).max(100).optional(),
})
export const discordThreadParams = z.strictObject({})
export const discordCursor = z.strictObject({
  guild: discordID,
  channel: discordID,
  before: discordID,
})

export function providerValue<T>(schema: z.ZodType<T>, value: JsonBody, mutation = false): T {
  const parsed = schema.safeParse(value)
  if (!parsed.success) throw new DiscordAPIError('invalid_response', { outcomeUnknown: mutation })
  return parsed.data
}
