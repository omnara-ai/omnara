import { DiscordAdapter } from '@chat-adapter/discord'
import type { JsonBody } from '@omnara/sdk'
import { type AdapterPostableMessage, ConsoleLogger } from 'chat'
import { z } from 'zod'

import { currentProviderOperation } from '../operations/provider-context'
import type { OperationAttemptContext } from '../operations/retry'
import type { DiscordConfiguration } from './configuration'
import { DiscordAPIError, discordChannel, discordMessage, providerValue } from './protocol'

type Request = (
  method: 'GET' | 'POST',
  path: string,
  context: OperationAttemptContext,
  body?: string,
) => Promise<JsonBody>
const rawMessage = z.object({ raw: z.string() })

/** Published @chat-adapter/discord 4.40.0, shared by one DiscordClient.
 * Async-local operation context carries each request's deadline and signal;
 * the adapter has no Chat runtime, state, socket or live webhook ownership.
 */
export class DiscordMessagingAdapter extends DiscordAdapter {
  constructor(
    private readonly configuration: Readonly<DiscordConfiguration>,
    private readonly request: Request,
  ) {
    super({
      botToken: configuration.botToken,
      applicationId: configuration.applicationID,
      // This adapter must never admit a webhook ahead of the durable inbox.
      webhookVerifier: () => false,
      logger: new ConsoleLogger('silent'),
    })
  }

  channelID(id: string): string {
    // A public thread is itself a Discord channel. Parent/guild authorization
    // is already checked by loadDiscordAddress; no SDK thread discovery needed.
    return this.encodeThreadId({ guildId: this.configuration.guildID, channelId: id })
  }

  protected override buildMessagePayload(message: AdapterPostableMessage) {
    // Even {raw} in 4.40.0 rewrites bare mentions and :emoji: inside code.
    // The gateway's text contract is already native Discord markdown. Retain
    // SDK posting/routing while bypassing just its lossy content conversion.
    const parsed = rawMessage.safeParse(message)
    if (!parsed.success) throw new DiscordAPIError('unsupported_message')
    return {
      componentCount: 0,
      embedCount: 0,
      payload: { content: parsed.data.raw, allowed_mentions: { parse: [] } },
    }
  }

  protected override async discordFetch(
    path: string,
    method: string,
    body?: Parameters<DiscordAdapter['discordFetch']>[2],
  ) {
    if ((method !== 'GET' && method !== 'POST') || !/^\/channels\/[1-9][0-9]*(?:\/|$)/.test(path))
      throw new DiscordAPIError('unsupported_request')
    // discordRequest validates JSON (including duplicate keys) and bounds it
    // before the SDK consumes the in-memory response. It also owns all errors,
    // so SDK NetworkError never obscures safe retries or unknown mutations.
    const value = await this.request(
      method,
      path.slice(1),
      currentProviderOperation(),
      body === undefined ? undefined : JSON.stringify(body),
    )
    // Validate before SDK field access: malformed successful POSTs are unknown
    // publications, while malformed reads remain permanent provider failures.
    const schema = method === 'POST' ? discordMessage : discordChannel
    return Response.json(providerValue(schema, value, method === 'POST'))
  }
}
