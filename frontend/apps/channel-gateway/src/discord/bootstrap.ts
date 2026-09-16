import type { ChannelConnectorAppConfiguration } from '@omnara/sdk'
import { z } from 'zod'

import {
  type OperationAttemptContext,
  type OperationRetryOptions,
  retryOperation,
} from '../operations/retry'
import { discordRequest } from './client'
import { discordAppCredentials } from './configuration'
import { DiscordAPIError, discordID, discordUser, providerValue } from './protocol'

export const discordIntents = 1 | (1 << 9) | (1 << 15) // Guilds, GuildMessages, MessageContent.
export const gatewayInfoSchema = z.object({
  url: z.url(),
  shards: z.number().int().positive(),
  session_start_limit: z
    .object({
      total: z.number().int().positive(),
      remaining: z.number().int().nonnegative(),
      reset_after: z.number().int().nonnegative(),
      max_concurrency: z.number().int().positive(),
    })
    .refine((limit) => limit.remaining <= limit.total),
})
export type DiscordGatewayInfo = z.infer<typeof gatewayInfoSchema>
const application = z.object({ id: discordID, flags: z.number().int().nonnegative() })

export function discordAPIURL(apiUrl = 'https://discord.com/api/v10/') {
  const url = new URL(apiUrl.endsWith('/') ? apiUrl : `${apiUrl}/`)
  if (
    url.username ||
    url.password ||
    url.search ||
    url.hash ||
    (url.protocol !== 'https:' &&
      !(url.protocol === 'http:' && ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)))
  )
    throw new DiscordAPIError('invalid_configuration')
  return url
}

export function validateDiscordGatewayURL(value: string, api: URL): void {
  const url = new URL(value)
  const local = api.protocol === 'http:' && url.protocol === 'ws:' && url.hostname === api.hostname
  if (
    url.username ||
    url.password ||
    url.search ||
    url.hash ||
    (!local &&
      (url.protocol !== 'wss:' ||
        !(url.hostname === 'gateway.discord.gg' || url.hostname.endsWith('.discord.gg'))))
  )
    throw new DiscordAPIError('invalid_gateway_url')
}

/** Recheck real app/token and privileged intent when a leased shard starts.
 * Go OAuth setup owns unit creation; this function performs only native reads.
 */
export async function inspectDiscordApplication(
  configuration: ChannelConnectorAppConfiguration,
  operation: Omit<OperationRetryOptions, 'idempotent'>,
  apiUrl?: string,
) {
  const credentials = discordAppCredentials(configuration)
  const api = discordAPIURL(apiUrl)
  const result = await retryOperation({ ...operation, idempotent: true }, (attempt) =>
    readApplication(credentials, api, attempt),
  )
  if (result instanceof DiscordAPIError) throw result
  return result
}

async function readApplication(
  credentials: ReturnType<typeof discordAppCredentials>,
  api: URL,
  attempt: OperationAttemptContext,
) {
  const app = providerValue(
    application,
    await discordRequest(api, credentials.botToken, 'GET', 'applications/@me', attempt),
  )
  if (app.id !== credentials.applicationID) return new DiscordAPIError('identity_mismatch')
  if (!(app.flags & ((1 << 18) | (1 << 19))))
    return new DiscordAPIError('message_content_intent_required')
  const user = providerValue(
    discordUser,
    await discordRequest(api, credentials.botToken, 'GET', 'users/@me', attempt),
  )
  if (user.bot !== true) return new DiscordAPIError('identity_mismatch')
  return { ...credentials, botUserID: user.id }
}

export async function fetchDiscordGatewayInfo(
  botToken: string,
  api: URL,
  attempt: OperationAttemptContext,
) {
  const info = providerValue(
    gatewayInfoSchema,
    await discordRequest(api, botToken, 'GET', 'gateway/bot', attempt),
  )
  validateDiscordGatewayURL(info.url, api)
  return info
}
